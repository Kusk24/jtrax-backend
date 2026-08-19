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

	"github.com/Kusk24/jtrax-backend/internal/auth"
	"github.com/Kusk24/jtrax-backend/internal/game"
	"github.com/Kusk24/jtrax-backend/internal/httpx"
)

// canPlay reports whether a role may sit down at a board. Parents are
// deliberately excluded: the feature exists so pupils play each other and so a
// teacher can demonstrate, and every seat taken is a seat a pupil cannot have.
func canPlay(role string) bool { return role == "Student" || role == "Teacher" }

// roomView is the JSON shape for a room. Codes are included only for callers
// who are entitled to one — see roomRow.view.
type roomView struct {
	ID        string `json:"gameRoomId"`
	Code      string `json:"code,omitempty"`
	Label     string `json:"label,omitempty"`
	Status    string `json:"status"`
	FEN       string `json:"fen"`
	Turn      string `json:"turn,omitempty"`
	Result    string `json:"result,omitempty"`
	Reason    string `json:"resultReason,omitempty"`
	White     *seat  `json:"white"`
	Black     *seat  `json:"black"`
	MoveCount int    `json:"moveCount"`
	CreatedAt string `json:"createdAt"`
	StartedAt string `json:"startedAt,omitempty"`
	EndedAt   string `json:"endedAt,omitempty"`

	// Whether this board is also a real rated game on lichess.org, and if it
	// stopped being one, why. The reason is carried to the players rather than
	// only logged: a game that has quietly stopped counting is worse than one
	// that says so.
	LichessRated    bool   `json:"lichessRated"`
	LichessGameID   string `json:"lichessGameId,omitempty"`
	LichessStatus   string `json:"lichessStatus,omitempty"`
	LichessDetached string `json:"lichessDetachedReason,omitempty"`
}

type seat struct {
	AccountID string `json:"userAccountId"`
	Name      string `json:"displayName"`
	StudentID string `json:"studentId,omitempty"`
}

type moveView struct {
	Ply       int    `json:"ply"`
	SAN       string `json:"san"`
	UCI       string `json:"uci"`
	FENAfter  string `json:"fenAfter"`
	CreatedAt string `json:"createdAt"`
}

// roomSelect resolves both seats to a display name and, where the account
// belongs to a pupil, a student id — so the admin history can say who played
// whom and link through to their record without a second round trip.
const roomSelect = `
SELECT r.game_room_id, r.code, COALESCE(r.label,''), r.status, r.fen,
       COALESCE(r.result,''), COALESCE(r.result_reason,''),
       COALESCE(r.white_account_id,''), COALESCE(wa.display_name,''), COALESCE(ws.student_id,''),
       COALESCE(r.black_account_id,''), COALESCE(ba.display_name,''), COALESCE(bs.student_id,''),
       (SELECT COUNT(*) FROM game_move m WHERE m.game_room_id = r.game_room_id),
       r.created_at, COALESCE(r.started_at,''), COALESCE(r.ended_at,''),
       r.lichess_rated, COALESCE(r.lichess_game_id,''), COALESCE(r.lichess_status,''),
       COALESCE(r.lichess_detached_reason,'')
FROM game_room r
LEFT JOIN user_account wa ON wa.user_account_id = r.white_account_id
LEFT JOIN student      ws ON ws.user_account_id = r.white_account_id
LEFT JOIN user_account ba ON ba.user_account_id = r.black_account_id
LEFT JOIN student      bs ON bs.user_account_id = r.black_account_id
`

type roomRow struct {
	roomView
	whiteID, blackID string
}

func scanRoom(sc interface{ Scan(...any) error }) (*roomRow, error) {
	var r roomRow
	var wName, wStu, bName, bStu string
	var rated int
	err := sc.Scan(&r.ID, &r.Code, &r.Label, &r.Status, &r.FEN, &r.Result, &r.Reason,
		&r.whiteID, &wName, &wStu, &r.blackID, &bName, &bStu,
		&r.MoveCount, &r.CreatedAt, &r.StartedAt, &r.EndedAt,
		&rated, &r.LichessGameID, &r.LichessStatus, &r.LichessDetached)
	if err != nil {
		return nil, err
	}
	r.LichessRated = rated == 1
	if r.whiteID != "" {
		r.White = &seat{AccountID: r.whiteID, Name: wName, StudentID: wStu}
	}
	if r.blackID != "" {
		r.Black = &seat{AccountID: r.blackID, Name: bName, StudentID: bStu}
	}
	return &r, nil
}

func (r *roomRow) seatOf(accountID string) string {
	switch accountID {
	case "":
		return ""
	case r.whiteID:
		return "White"
	case r.blackID:
		return "Black"
	}
	return ""
}

// stripCode blanks the join code for callers who should not be handed one.
//
// A code is a bearer credential: anyone holding it can take the free seat. Staff
// need it to read out, and a seated player may want to pass it to their
// opponent, but a finished room's code is spent and nobody else's code is any
// caller's business.
func (r *roomRow) view(id *auth.Identity) roomView {
	v := r.roomView
	if !isStaff(id.Role) && r.seatOf(id.UserAccountID) == "" {
		v.Code = ""
	}
	return v
}

// loadRoom fetches a room by id and reports whether the caller may see it.
func loadRoom(d *sql.DB, roomID string) (*roomRow, error) {
	return scanRoom(d.QueryRow(roomSelect+" WHERE r.game_room_id = ?", roomID))
}

func roomMoves(d *sql.DB, roomID string) ([]moveView, []string, error) {
	rows, err := d.Query(`SELECT ply, san, uci, fen_after, created_at FROM game_move
	                      WHERE game_room_id = ? ORDER BY ply`, roomID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	views := []moveView{}
	ucis := []string{}
	for rows.Next() {
		var m moveView
		if err := rows.Scan(&m.Ply, &m.SAN, &m.UCI, &m.FENAfter, &m.CreatedAt); err != nil {
			return nil, nil, err
		}
		views = append(views, m)
		ucis = append(ucis, m.UCI)
	}
	return views, ucis, rows.Err()
}

// handleCreateRoom mints a room. Staff only — the console hands out codes.
func handleCreateRoom(d *sql.DB) http.HandlerFunc {
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
			Rated          bool `json:"lichessRated"`
			ClockLimit     int  `json:"clockLimit"`
			ClockIncrement int  `json:"clockIncrement"`
		}
		if err := httpx.Decode(r, &in); err != nil && err.Error() != "EOF" {
			httpx.Error(w, http.StatusBadRequest, "invalid body", err)
			return
		}
		if len(in.Label) > 80 {
			httpx.Error(w, http.StatusBadRequest, "label is too long", nil)
			return
		}
		limit, increment, err := lichessClock(in.ClockLimit, in.ClockIncrement)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, err.Error(), nil)
			return
		}
		rated := 0
		if in.Rated {
			rated = 1
		}
		// Retry on the unique-code collision rather than checking first, which
		// would race two concurrent creates onto the same code.
		var roomID string
		for attempt := 0; attempt < 5; attempt++ {
			code, cerr := game.Code()
			if cerr != nil {
				httpx.Error(w, http.StatusInternalServerError, "could not create room", cerr)
				return
			}
			roomID = newID("gmr")
			_, err = d.Exec(`INSERT INTO game_room (game_room_id, code, label, created_by, fen,
			                                        lichess_rated, lichess_clock_limit, lichess_clock_increment)
			                 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
				roomID, code, strings.TrimSpace(in.Label), id.UserAccountID, game.StartFEN,
				rated, limit, increment)
			if err == nil {
				break
			}
		}
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not create room", err)
			return
		}
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
		// seat rather than being told the room is full.
		if s := room.seatOf(id.UserAccountID); s != "" {
			httpx.JSON(w, http.StatusOK, map[string]any{"room": room.view(id), "seat": s})
			return
		}
		if room.Status != "Open" {
			httpx.Error(w, http.StatusConflict, "that room is no longer open", nil)
			return
		}

		// White first, then black; the second claim also fills the room, so it
		// flips the status and starts the clock in the same statement.
		res, err := d.Exec(`UPDATE game_room SET white_account_id = ?
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
			                   SET black_account_id = ?, status = 'Active', started_at = datetime('now')
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

// handleMove grades and records one half-move.
func handleMove(d *sql.DB, h *hub, relay *lichessRelay) http.HandlerFunc {
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
			// Staff can watch a game; nobody may play someone else's side.
			httpx.Error(w, http.StatusForbidden, "you are not playing in this game", nil)
			return
		}
		if room.Status != "Active" {
			httpx.Error(w, http.StatusConflict, "that game is not in play", nil)
			return
		}
		var in struct {
			Move string `json:"move"`
		}
		if err := httpx.Decode(r, &in); err != nil || strings.TrimSpace(in.Move) == "" {
			httpx.Error(w, http.StatusBadRequest, "a move is required", err)
			return
		}
		if len(in.Move) > 6 {
			httpx.Error(w, http.StatusBadRequest, "that is not a move", nil)
			return
		}
		moves, ucis, err := roomMoves(d, room.ID)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "query failed", err)
			return
		}
		// Whose turn it is comes from the position, not from the move count, so
		// there is one source of truth for turn order.
		before, err := game.Describe(ucis)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not replay game", err)
			return
		}
		if before.Turn != mySeat {
			httpx.Error(w, http.StatusConflict, "it is not your turn", nil)
			return
		}
		applied, err := game.Apply(ucis, in.Move)
		if err != nil {
			if errors.Is(err, game.ErrIllegalMove) {
				httpx.Error(w, http.StatusBadRequest, "that move is not legal here", nil)
				return
			}
			httpx.Error(w, http.StatusInternalServerError, "could not apply move", err)
			return
		}

		ply := len(moves) + 1
		// The (room, ply) primary key is the referee for a race: if the same
		// player double-submits, or a stale client replays, the second insert
		// fails here instead of appending a second move to one turn.
		if _, err := d.Exec(`INSERT INTO game_move (game_room_id, ply, san, uci, fen_after)
		                     VALUES (?, ?, ?, ?, ?)`,
			room.ID, ply, applied.SAN, applied.UCI, applied.FEN); err != nil {
			httpx.Error(w, http.StatusConflict, "the position has already moved on", err)
			return
		}
		if applied.Result != "" {
			_, err = d.Exec(`UPDATE game_room SET fen = ?, status = 'Finished',
			                 result = ?, result_reason = ?, ended_at = datetime('now')
			                 WHERE game_room_id = ?`,
				applied.FEN, applied.Result, applied.Reason, room.ID)
		} else {
			_, err = d.Exec(`UPDATE game_room SET fen = ? WHERE game_room_id = ?`, applied.FEN, room.ID)
		}
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "move stored but room not updated", err)
			return
		}
		publishRoom(d, h, room.ID)
		// Forwarded after the move is stored and published, never before: the
		// pupil's own piece must not wait on a round trip to lichess.org.
		relay.forward(room.ID, mySeat, applied.UCI)

		after, err := game.Describe(append(ucis, applied.UCI))
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not replay game", err)
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]any{
			"move": moveView{Ply: ply, SAN: applied.SAN, UCI: applied.UCI, FENAfter: applied.FEN},
			"fen":  applied.FEN, "turn": after.Turn, "check": applied.Check,
			"result": applied.Result, "resultReason": applied.Reason,
			"legalMoves": after.Legal,
		})
	}
}

// handleResign ends a game in the opponent's favour. The resigning colour comes
// from the seat the caller holds, so nobody can resign on another player's
// behalf by naming a colour in the body.
func handleResign(d *sql.DB, h *hub, relay *lichessRelay) http.HandlerFunc {
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
			httpx.Error(w, http.StatusForbidden, "you are not playing in this game", nil)
			return
		}
		if room.Status != "Active" {
			httpx.Error(w, http.StatusConflict, "that game is not in play", nil)
			return
		}
		_, ucis, err := roomMoves(d, room.ID)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "query failed", err)
			return
		}
		result, reason, err := game.Resign(ucis, mySeat)
		if err != nil {
			httpx.Error(w, http.StatusConflict, "that game is not in play", err)
			return
		}
		if _, err := d.Exec(`UPDATE game_room SET status = 'Finished', result = ?,
		                     result_reason = ?, ended_at = datetime('now')
		                     WHERE game_room_id = ? AND status = 'Active'`,
			result, reason, room.ID); err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not record the resignation", err)
			return
		}
		publishRoom(d, h, room.ID)
		// Resigning here has to resign there too. Without this the Lichess game
		// would run on until it timed out, and the rating would move minutes
		// later for a reason neither pupil would recognise.
		relay.resign(room.ID, mySeat)
		httpx.JSON(w, http.StatusOK, map[string]any{"result": result, "resultReason": reason})
	}
}

// handleCancelRoom pulls a room. Staff only, and it never deletes: a played
// game is a record the academy keeps, so cancelling marks it and stops play.
func handleCancelRoom(d *sql.DB, h *hub, relay *lichessRelay) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := requireIdentity(d, w, r)
		if id == nil {
			return
		}
		if !isStaff(id.Role) {
			httpx.Error(w, http.StatusForbidden, "not allowed", nil)
			return
		}
		res, err := d.Exec(`UPDATE game_room SET status = 'Cancelled', ended_at = datetime('now')
		                    WHERE game_room_id = ? AND status IN ('Open','Active')`, r.PathValue("id"))
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not cancel room", err)
			return
		}
		if n, _ := res.RowsAffected(); n == 0 {
			httpx.Error(w, http.StatusNotFound, "no room to cancel", nil)
			return
		}
		// Otherwise the Lichess game runs on with nobody watching it and this
		// server keeps a stream goroutine open for a board that is gone.
		relay.abandon(r.PathValue("id"))
		publishRoom(d, h, r.PathValue("id"))
		httpx.JSON(w, http.StatusOK, map[string]string{"status": "cancelled"})
	}
}

// mountGameRooms returns the relay it built, so an accepted challenge can start
// a rated game through the same one. Two relays would mean two watchers on the
// same board and two sets of moves forwarded to Lichess.
func mountGameRooms(mux *http.ServeMux, d *sql.DB) *lichessRelay {
	h := newHub()
	relay := newLichessRelay(d, h, lichessOAuthFromEnv(d))
	// Rated games that were in play when this process last stopped still need
	// watching; on the free tier a restart mid-lesson is routine.
	relay.resume()
	const base = "/api/v1/game-rooms"
	mux.HandleFunc("GET "+base, handleListRooms(d))
	mux.HandleFunc("POST "+base, handleCreateRoom(d))
	// Joining is the one endpoint where a caller supplies a secret they might
	// be guessing, so it carries a tighter budget than the rest.
	mux.HandleFunc("POST "+base+"/join", httpx.RateLimit(20, handleJoinRoom(d, h, relay)))
	mux.HandleFunc("GET "+base+"/{id}", handleGetRoom(d))
	mux.HandleFunc("DELETE "+base+"/{id}", handleCancelRoom(d, h, relay))
	mux.HandleFunc("POST "+base+"/{id}/moves", handleMove(d, h, relay))
	mux.HandleFunc("POST "+base+"/{id}/resign", handleResign(d, h, relay))
	mux.HandleFunc("GET "+base+"/{id}/events", handleRoomEvents(d, h))
	return relay
}
