// Live game updates over Server-Sent Events.
//
// SSE rather than WebSockets: chess is turn-based at roughly one move every few
// seconds, so full duplex buys nothing, and this is ~100 lines of net/http
// instead of a protocol upgrade. It matters more that EventSource reconnects on
// its own — the API sleeps after fifteen idle minutes on the free tier, and a
// dropped stream has to heal without the player noticing.
//
// The hub is in-process. That is correct for a single instance and wrong the
// moment there are two: a move made on instance A would not reach a watcher on
// instance B. Scaling out means moving this to a shared bus, and the failure is
// silent, so it is called out in docs/game-rooms.md too.
package chessplay

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/Kusk24/jtrax-backend/internal/accounts"
	"github.com/Kusk24/jtrax-backend/internal/game"
	"github.com/Kusk24/jtrax-backend/internal/httpx"
	"github.com/Kusk24/jtrax-backend/internal/liveevents"
)

// roomEvent is what watchers receive. It deliberately carries no room code:
// the stream reaches staff and both players, and a code is a credential that
// belongs in an authorized read, not in a broadcast.
type roomEvent struct {
	RoomID   string `json:"gameRoomId"`
	Status   string `json:"status"`
	FEN      string `json:"fen"`
	Turn     string `json:"turn,omitempty"`
	Ply      int    `json:"ply"`
	LastSAN  string `json:"lastSan,omitempty"`
	LastUCI  string `json:"lastUci,omitempty"`
	Result   string `json:"result,omitempty"`
	Reason   string `json:"resultReason,omitempty"`
	White    *seat  `json:"white"`
	Black    *seat  `json:"black"`
	Finished bool   `json:"finished"`
	// Removed says the room itself is gone — sent once, as it is deleted.
	Removed bool `json:"removed,omitempty"`
	// A draw offer standing, and a rated game's clock — both change without a
	// move being played, so a board learns them from here.
	DrawOffer string     `json:"drawOffer,omitempty"`
	Clock     *clockView `json:"clock,omitempty"`
}

// publishRoom reads the room back and fans out its current state.
//
// It publishes a snapshot rather than a delta so a watcher that missed an event
// still converges on the truth, and so a late joiner needs no replay.
func publishRoom(d *sql.DB, h *liveevents.Hub, roomID string) {
	room, err := loadRoom(d, roomID)
	if err != nil {
		return
	}
	moves, ucis, err := roomMoves(d, roomID)
	if err != nil {
		return
	}
	ev := roomEvent{
		RoomID: room.ID, Status: room.Status, FEN: room.FEN,
		Result: room.Result, Reason: room.Reason,
		White: room.White, Black: room.Black,
		Ply:       len(moves),
		Finished:  room.Status == "Finished" || room.Status == "Cancelled",
		DrawOffer: room.DrawOffer,
		Clock:     room.Clock,
	}
	if n := len(moves); n > 0 {
		ev.LastSAN, ev.LastUCI = moves[n-1].SAN, moves[n-1].UCI
	}
	if st, err := game.Describe(ucis); err == nil {
		ev.Turn = st.Turn
	}
	payload, err := json.Marshal(ev)
	if err != nil {
		return
	}
	h.Broadcast(roomID, payload)
}

// handleRoomEvents streams a room's state changes to staff and to the two
// players. Anyone else gets the same 404 the read endpoint gives, so a room id
// cannot be probed through the stream either.
func handleRoomEvents(d *sql.DB, h *liveevents.Hub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := accounts.RequireIdentity(d, w, r)
		if id == nil {
			return
		}
		roomID := r.PathValue("id")
		room, err := loadRoom(d, roomID)
		if err != nil {
			httpx.Error(w, http.StatusNotFound, "not found", nil)
			return
		}
		if !accounts.IsStaff(id.Role) && room.seatOf(id.UserAccountID) == "" {
			httpx.Error(w, http.StatusNotFound, "not found", nil)
			return
		}
		flusher, ok := w.(http.Flusher)
		if !ok {
			httpx.Error(w, http.StatusInternalServerError, "streaming unavailable", nil)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		// Tells nginx-style proxies not to buffer, which would hold every event
		// until the response ended — that is, until the game did.
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)

		ch := h.Subscribe(roomID)
		defer h.Unsubscribe(roomID, ch)

		// Send the current state immediately, so a client that connects mid-game
		// — or reconnects after a sleep — is correct without a separate fetch.
		publishRoom(d, h, roomID)

		ticker := time.NewTicker(liveevents.Heartbeat)
		defer ticker.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case payload := <-ch:
				fmt.Fprintf(w, "event: room\ndata: %s\n\n", payload)
				flusher.Flush()
			case <-ticker.C:
				// A comment line: valid SSE, ignored by EventSource, enough to
				// keep the connection from being reaped as idle.
				fmt.Fprint(w, ": ping\n\n")
				flusher.Flush()
			}
		}
	}
}
