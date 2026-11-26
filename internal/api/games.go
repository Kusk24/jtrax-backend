// Game-room endpoints: staff mint a room and hand out its code, two signed-in
// players claim its seats, and every move is graded here before it is stored.
//
// These are hand-written rather than declared in the registry because none of
// the three interesting operations is CRUD: claiming a seat is a race that has
// to be settled atomically, a move has to be legal in the position it is played
// from, and a room code is a credential rather than a field.
package api

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"github.com/Kusk24/jtrax-backend/internal/game"
	"github.com/Kusk24/jtrax-backend/internal/httpx"
)

// canPlay reports whether a role may sit down at a board. Parents are
// deliberately excluded: the feature exists so pupils play each other and so a
// teacher can demonstrate, and every seat taken is a seat a pupil cannot have.
func canPlay(role string) bool { return role == "Student" || role == "Teacher" }

// handleCreateRoom mints a room. Staff only — the console hands out codes.
//
// The console can also seat the players itself: name a White and a Black
// student and the game opens already in play, the way an accepted challenge
// does, and appears in both pupils' own game lists — nobody has to type a
// code. Without them it is the old room: a code to read out, first two in.
func handleCreateRoom(d *sql.DB, relay *lichessRelay) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := requireIdentity(d, w, r)
		if id == nil {
			return
		}
		if !isStaff(id.Role) {
			httpx.Error(w, http.StatusForbidden, "not allowed", nil)
			return
		}
		var in struct {
			Label string `json:"label"`
			// Rated makes this board a real game on lichess.org. Opt-in per
			// room, because it needs both pupils to have granted play access
			// and because a lesson game should not move a child's rating.
			Rated bool `json:"lichessRated"`
			// Timed says a time control was chosen. A rated game always has
			// one; an unrated one may. Older consoles send only the clock for
			// a rated room, which still counts.
			Timed          bool   `json:"timed"`
			ClockLimit     int    `json:"clockLimit"`
			ClockIncrement int    `json:"clockIncrement"`
			WhiteStudentID string `json:"whiteStudentId"`
			BlackStudentID string `json:"blackStudentId"`
		}
		if err := httpx.Decode(r, &in); err != nil && err.Error() != "EOF" {
			httpx.Error(w, http.StatusBadRequest, "invalid body", err)
			return
		}
		if len(in.Label) > 80 {
			httpx.Error(w, http.StatusBadRequest, "label is too long", nil)
			return
		}
		timed := in.Rated || in.Timed
		limit, increment, err := lichessClock(in.ClockLimit, in.ClockIncrement)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, err.Error(), nil)
			return
		}

		// Both players or neither: one seat filled by the office and the other
		// left to a code would leave the named pupil waiting on a stranger.
		var white, black string
		if in.WhiteStudentID != "" || in.BlackStudentID != "" {
			if in.WhiteStudentID == "" || in.BlackStudentID == "" {
				httpx.Error(w, http.StatusBadRequest, "choose both players, or neither", nil)
				return
			}
			if in.WhiteStudentID == in.BlackStudentID {
				httpx.Error(w, http.StatusBadRequest, "a student cannot play themselves", nil)
				return
			}
			for _, p := range []struct {
				studentID string
				into      *string
			}{{in.WhiteStudentID, &white}, {in.BlackStudentID, &black}} {
				err := d.QueryRow(`SELECT COALESCE(user_account_id,'') FROM student
				                   WHERE student_id = ?`, p.studentID).Scan(p.into)
				if errors.Is(err, sql.ErrNoRows) {
					httpx.Error(w, http.StatusBadRequest, "no such student", nil)
					return
				}
				if err != nil {
					httpx.Error(w, http.StatusInternalServerError, "could not read student", err)
					return
				}
				if *p.into == "" {
					httpx.Error(w, http.StatusBadRequest, "that student has no sign-in yet", nil)
					return
				}
			}
		}

		tx, err := d.Begin()
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not create room", err)
			return
		}
		defer tx.Rollback()
		// One board at a time. Checked inside the transaction that seats them,
		// so two games started at once for the same child cannot both pass.
		if white != "" {
			var seated int
			if err := tx.QueryRow(`SELECT COUNT(*) FROM game_room
			                       WHERE status IN ('Open','Active')
			                         AND (white_account_id IN (?, ?) OR black_account_id IN (?, ?))`,
				white, black, white, black).Scan(&seated); err != nil {
				httpx.Error(w, http.StatusInternalServerError, "could not create room", err)
				return
			}
			if seated > 0 {
				httpx.Error(w, http.StatusConflict, "that student is already in another game", nil)
				return
			}
		}
		roomID, err := mintRoom(tx, mintOptions{
			CreatedBy: id.UserAccountID, Label: in.Label,
			White: white, Black: black, Assigned: white != "",
			Rated: in.Rated, Timed: timed, ClockLimit: limit, ClockIncrement: increment,
		})
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not create room", err)
			return
		}
		if err := tx.Commit(); err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not create room", err)
			return
		}
		// Seated but not started: the game — and on a rated one, the Lichess
		// pairing — begins when both students press Enter.
		room, err := loadRoom(d, roomID)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "created but could not reload", err)
			return
		}
		httpx.JSON(w, http.StatusCreated, room.view(id))
	}
}

// handleListRooms returns every room for staff, and only the caller's own
// boards for a player. The restriction is in the WHERE clause, not applied to
// results afterwards, so a player's own list can never contain someone else's
// room code.
func handleListRooms(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := requireIdentity(d, w, r)
		if id == nil {
			return
		}
		q := roomSelect
		args := []any{}
		if !isStaff(id.Role) {
			q += " WHERE (r.white_account_id = ? OR r.black_account_id = ?)"
			args = append(args, id.UserAccountID, id.UserAccountID)
		} else if s := r.URL.Query().Get("status"); s != "" {
			q += " WHERE r.status = ?"
			args = append(args, s)
		}
		q += " ORDER BY r.created_at DESC, r.game_room_id DESC"
		rows, err := d.Query(q, args...)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "query failed", err)
			return
		}
		defer rows.Close()
		out := []roomView{}
		for rows.Next() {
			room, err := scanRoom(rows)
			if err != nil {
				httpx.Error(w, http.StatusInternalServerError, "query failed", err)
				return
			}
			out = append(out, room.view(id))
		}
		if err := rows.Close(); err != nil {
			httpx.Error(w, http.StatusInternalServerError, "query failed", err)
			return
		}
		// The console's wall of boards shows each game's moves, so it asks for
		// them here rather than opening every room one by one.
		if isStaff(id.Role) && r.URL.Query().Get("moves") == "1" {
			for i := range out {
				moves, _, err := roomMoves(d, out[i].ID)
				if err != nil {
					httpx.Error(w, http.StatusInternalServerError, "query failed", err)
					return
				}
				out[i].SANs = make([]string, len(moves))
				for j, m := range moves {
					out[i].SANs[j] = m.SAN
				}
				if n := len(moves); n > 0 {
					out[i].LastUCI = moves[n-1].UCI
				}
			}
		}
		httpx.JSON(w, http.StatusOK, out)
	}
}

// handleGetRoom returns one room with its full move list and, for a player, the
// moves they may legally make.
func handleGetRoom(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := requireIdentity(d, w, r)
		if id == nil {
			return
		}
		room, err := loadRoom(d, r.PathValue("id"))
		if err != nil {
			httpx.Error(w, http.StatusNotFound, "not found", nil)
			return
		}
		mySeat := room.seatOf(id.UserAccountID)
		if !isStaff(id.Role) && mySeat == "" {
			// Same answer as a room that does not exist: a player probing ids
			// should not be able to tell a real room from a missing one.
			httpx.Error(w, http.StatusNotFound, "not found", nil)
			return
		}
		moves, ucis, err := roomMoves(d, room.ID)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "query failed", err)
			return
		}
		status, err := game.Describe(ucis)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not replay game", err)
			return
		}
		v := room.view(id)
		v.Turn = status.Turn
		httpx.JSON(w, http.StatusOK, map[string]any{
			"room":       v,
			"seat":       mySeat,
			"moves":      moves,
			"legalMoves": status.Legal,
		})
	}
}

// handleJoinRoom claims a seat for the caller.
//
// The claim is a conditional UPDATE, not a read-then-write: two students
// submitting the same code at the same instant both pass any check performed in
// Go, so the free-seat test has to be the WHERE clause the database evaluates.
func handleJoinRoom(d *sql.DB, h *hub, relay *lichessRelay) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := requireIdentity(d, w, r)
		if id == nil {
			return
		}
		if !canPlay(id.Role) {
			httpx.Error(w, http.StatusForbidden, "only students and teachers can take a seat", nil)
			return
		}
		var in struct {
			Code string `json:"code"`
		}
		if err := httpx.Decode(r, &in); err != nil {
			httpx.Error(w, http.StatusBadRequest, "a room code is required", err)
			return
		}
		code := strings.ToUpper(strings.TrimSpace(in.Code))
		if code == "" {
			httpx.Error(w, http.StatusBadRequest, "a room code is required", nil)
			return
		}
		room, err := scanRoom(d.QueryRow(roomSelect+" WHERE r.code = ?", code))
		if err != nil {
			httpx.Error(w, http.StatusNotFound, "no open room with that code", nil)
			return
		}
		// Rejoining is not joining: a player who reloaded the page keeps their
		// seat rather than being told the room is full. A player the office
		// seated who types the code instead of pressing Enter has entered.
		if s := room.seatOf(id.UserAccountID); s != "" {
			if err := enterSeat(d, h, relay, room.ID, s); err != nil {
				httpx.Error(w, http.StatusInternalServerError, "could not enter the game", err)
				return
			}
			if fresh, err := loadRoom(d, room.ID); err == nil {
				room = fresh
			}
			httpx.JSON(w, http.StatusOK, map[string]any{"room": room.view(id), "seat": s})
			return
		}
		if room.Status != "Open" {
			httpx.Error(w, http.StatusConflict, "that room is no longer open", nil)
			return
		}

		// White first, then black; the second claim also fills the room, so it
		// flips the status and starts the clock in the same statement.
		res, err := d.Exec(`UPDATE game_room SET white_account_id = ?, white_entered_at = datetime('now')
		                    WHERE game_room_id = ? AND white_account_id IS NULL`,
			id.UserAccountID, room.ID)
		seatTaken := ""
		if err == nil {
			if n, _ := res.RowsAffected(); n == 1 {
				seatTaken = "White"
			}
		}
		if seatTaken == "" {
			res, err = d.Exec(`UPDATE game_room
			                   SET black_account_id = ?, black_entered_at = datetime('now'),
			                       status = 'Active', started_at = datetime('now')
			                   WHERE game_room_id = ? AND black_account_id IS NULL
			                     AND white_account_id IS NOT NULL AND white_account_id <> ?`,
				id.UserAccountID, room.ID, id.UserAccountID)
			if err == nil {
				if n, _ := res.RowsAffected(); n == 1 {
					seatTaken = "Black"
				}
			}
		}
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not join room", err)
			return
		}
		if seatTaken == "" {
			httpx.Error(w, http.StatusConflict, "that room already has two players", nil)
			return
		}
		// The second seat is what starts the game, so it is also what pairs the
		// two pupils on Lichess. Done before the reload so the response already
		// says whether the board really is rated — a screen that promises a
		// rated game and then withdraws it a second later is worse than one
		// that never promised.
		if seatTaken == "Black" {
			var rated int
			if err := d.QueryRow(`SELECT lichess_rated FROM game_room WHERE game_room_id = ?`,
				room.ID).Scan(&rated); err == nil && rated == 1 {
				relay.begin(room.ID)
			}
		}

		fresh, err := loadRoom(d, room.ID)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "joined but could not reload", err)
			return
		}
		// The opponent's board learns a player sat down without polling for it.
		publishRoom(d, h, room.ID)
		httpx.JSON(w, http.StatusOK, map[string]any{"room": fresh.view(id), "seat": seatTaken})
	}
}

// enterSeat records that a seated player has sat down at the board, and starts
// the game when both have. Both steps are conditional UPDATEs, so two players
// pressing Enter at the same instant start the game exactly once.
func enterSeat(d *sql.DB, h *hub, relay *lichessRelay, roomID, seat string) error {
	col := "white_entered_at"
	if seat == "Black" {
		col = "black_entered_at"
	}
	// A paused game is the office's to resume, not the players'.
	if _, err := d.Exec(`UPDATE game_room SET `+col+` = datetime('now')
	                     WHERE game_room_id = ? AND status = 'Open' AND stopped_at IS NULL AND `+col+` IS NULL`, roomID); err != nil {
		return err
	}
	// A resumed game keeps the day it began; one starting now gets today.
	res, err := d.Exec(`UPDATE game_room SET status = 'Active', started_at = COALESCE(started_at, datetime('now'))
	                    WHERE game_room_id = ? AND status = 'Open' AND stopped_at IS NULL
	                      AND white_account_id IS NOT NULL AND black_account_id IS NOT NULL
	                      AND white_entered_at IS NOT NULL AND black_entered_at IS NOT NULL`, roomID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 1 {
		// Both are at the board, so this is the moment a rated game is paired
		// on Lichess — not when the office set it up.
		var rated int
		if err := d.QueryRow(`SELECT lichess_rated FROM game_room WHERE game_room_id = ?`,
			roomID).Scan(&rated); err == nil && rated == 1 {
			relay.begin(roomID)
		}
	}
	publishRoom(d, h, roomID)
	return nil
}

// handleEnterRoom is a seated player pressing Enter on a game the office set up
// for them. The game is in play once both have; until then it waits, Open.
// Pressing it again, or on a game already in play, is harmless.
func handleEnterRoom(d *sql.DB, h *hub, relay *lichessRelay) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := requireIdentity(d, w, r)
		if id == nil {
			return
		}
		room, err := loadRoom(d, r.PathValue("id"))
		if err != nil {
			httpx.Error(w, http.StatusNotFound, "not found", nil)
			return
		}
		mySeat := room.seatOf(id.UserAccountID)
		if mySeat == "" {
			// Same answer as a room that does not exist.
			httpx.Error(w, http.StatusNotFound, "not found", nil)
			return
		}
		if room.Status == "Finished" || room.Status == "Cancelled" {
			httpx.Error(w, http.StatusConflict, "that game is over", nil)
			return
		}
		if room.Stopped {
			httpx.Error(w, http.StatusConflict, "that game is paused", nil)
			return
		}
		if err := enterSeat(d, h, relay, room.ID, mySeat); err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not enter the game", err)
			return
		}
		fresh, err := loadRoom(d, room.ID)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "entered but could not reload", err)
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"room": fresh.view(id), "seat": mySeat})
	}
}

// mountGameRooms returns the relay it built, so an accepted challenge can start
// a rated game through the same one. Two relays would mean two watchers on the
// same board and two sets of moves forwarded to Lichess.
func mountGameRooms(mux routeRegistrar, d *sql.DB) *lichessRelay {
	h := newHub()
	relay := newLichessRelay(d, h, lichessOAuthFromEnv(d))
	// Rated games that were in play when this process last stopped still need
	// watching; on the free tier a restart mid-lesson is routine.
	relay.resume()
	const base = "/api/v1/game-rooms"
	mux.HandleFunc("GET "+base, handleListRooms(d))
	mux.HandleFunc("POST "+base, handleCreateRoom(d, relay))
	// Joining is the one endpoint where a caller supplies a secret they might
	// be guessing, so it carries a tighter budget than the rest.
	mux.HandleFunc("POST "+base+"/join", httpx.RateLimit(20, handleJoinRoom(d, h, relay)))
	mux.HandleFunc("GET "+base+"/{id}", handleGetRoom(d))
	// Cancel keeps DELETE /{id} — an older console sends it for "Stop this
	// game", and a deploy window where that destroys a board is not a thing to
	// invite. Throwing the record away is its own path.
	mux.HandleFunc("DELETE "+base+"/{id}", handleCancelRoom(d, h, relay))
	mux.HandleFunc("DELETE "+base+"/{id}/record", handleDeleteRoom(d, h, relay))
	mux.HandleFunc("POST "+base+"/{id}/enter", handleEnterRoom(d, h, relay))
	mux.HandleFunc("POST "+base+"/{id}/stop", handleStopRoom(d, h, relay))
	mux.HandleFunc("POST "+base+"/{id}/resume", handleResumeRoom(d, h))
	mux.HandleFunc("POST "+base+"/{id}/moves", handleMove(d, h, relay))
	mux.HandleFunc("POST "+base+"/{id}/resign", handleResign(d, h, relay))
	mux.HandleFunc("POST "+base+"/{id}/draw/offer", handleDraw(d, h, relay, "offer"))
	mux.HandleFunc("POST "+base+"/{id}/draw/accept", handleDraw(d, h, relay, "accept"))
	mux.HandleFunc("POST "+base+"/{id}/draw/decline", handleDraw(d, h, relay, "decline"))
	mux.HandleFunc("GET "+base+"/{id}/events", handleRoomEvents(d, h))
	return relay
}
