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
	"encoding/json"
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

	// The time control chosen for the game, absent when none was. Only a rated
	// game's clock actually runs — Lichess keeps it — so Clock is set only
	// there; an unrated game shows its time control as a label.
	TimeControl *timeControl `json:"timeControl,omitempty"`
	Clock       *clockView   `json:"clock,omitempty"`
	// The colour offering a draw, while the offer stands.
	DrawOffer string `json:"drawOffer,omitempty"`
	// Whether each seated player has entered the room. A game the office set
	// up waits, Open, until both have.
	WhiteEntered bool `json:"whiteEntered"`
	BlackEntered bool `json:"blackEntered"`
	// Paused by the office mid-game: Open again with its moves kept, and
	// nobody can move until the office resumes it.
	Stopped bool `json:"stopped"`

	// The moves in SAN and the last one in UCI — only on a staff list read
	// that asks for them (?moves=1), for the console's wall of boards.
	SANs    []string `json:"sans,omitempty"`
	LastUCI string   `json:"lastUci,omitempty"`
}

type timeControl struct {
	Limit     int `json:"limit"`
	Increment int `json:"increment"`
}

// clockView is each side's remaining time as Lichess last reported it, and
// when. The side to move has been counting down since `at`.
type clockView struct {
	WhiteMs int64  `json:"whiteMs"`
	BlackMs int64  `json:"blackMs"`
	At      string `json:"at"`
}

type seat struct {
	AccountID string `json:"userAccountId"`
	Name      string `json:"displayName"`
	StudentID string `json:"studentId,omitempty"`
	// The player's Lichess rating in the speed this game is played at, when
	// they have one that is not provisional. Omitted rather than 0 otherwise.
	Rating int `json:"rating,omitempty"`
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
       COALESCE(r.lichess_detached_reason,''),
       r.timed, r.lichess_clock_limit, r.lichess_clock_increment,
       r.clock_white_ms, r.clock_black_ms, COALESCE(r.clock_at,''), COALESCE(r.draw_offer,''),
       r.white_entered_at IS NOT NULL, r.black_entered_at IS NOT NULL, r.stopped_at IS NOT NULL,
       COALESCE((SELECT lr.rating FROM lichess_rating lr
                 WHERE lr.student_id = ws.student_id AND lr.provisional = 0 AND lr.perf = ` + roomPerf + `), 0),
       COALESCE((SELECT lr.rating FROM lichess_rating lr
                 WHERE lr.student_id = bs.student_id AND lr.provisional = 0 AND lr.perf = ` + roomPerf + `), 0)
FROM game_room r
LEFT JOIN user_account wa ON wa.user_account_id = r.white_account_id
LEFT JOIN student      ws ON ws.user_account_id = r.white_account_id
LEFT JOIN user_account ba ON ba.user_account_id = r.black_account_id
LEFT JOIN student      bs ON bs.user_account_id = r.black_account_id
`

// roomPerf is the Lichess speed a room is played at, by Lichess's own rule:
// the limit plus forty increments. A game with no time control reads as
// rapid, the speed a lesson game is closest to.
const roomPerf = `(CASE
    WHEN r.timed = 0 THEN 'rapid'
    WHEN r.lichess_clock_limit + 40 * r.lichess_clock_increment < 180 THEN 'bullet'
    WHEN r.lichess_clock_limit + 40 * r.lichess_clock_increment < 480 THEN 'blitz'
    WHEN r.lichess_clock_limit + 40 * r.lichess_clock_increment < 1500 THEN 'rapid'
    ELSE 'classical' END)`

type roomRow struct {
	roomView
	whiteID, blackID string
}

func scanRoom(sc interface{ Scan(...any) error }) (*roomRow, error) {
	var r roomRow
	var wName, wStu, bName, bStu string
	var rated, timed, limit, increment, wRating, bRating int
	var wMs, bMs sql.NullInt64
	var clockAt string
	err := sc.Scan(&r.ID, &r.Code, &r.Label, &r.Status, &r.FEN, &r.Result, &r.Reason,
		&r.whiteID, &wName, &wStu, &r.blackID, &bName, &bStu,
		&r.MoveCount, &r.CreatedAt, &r.StartedAt, &r.EndedAt,
		&rated, &r.LichessGameID, &r.LichessStatus, &r.LichessDetached,
		&timed, &limit, &increment, &wMs, &bMs, &clockAt, &r.DrawOffer,
		&r.WhiteEntered, &r.BlackEntered, &r.Stopped, &wRating, &bRating)
	if err != nil {
		return nil, err
	}
	r.LichessRated = rated == 1
	if timed == 1 {
		r.TimeControl = &timeControl{Limit: limit, Increment: increment}
	}
	if r.LichessRated && wMs.Valid && bMs.Valid {
		r.Clock = &clockView{WhiteMs: wMs.Int64, BlackMs: bMs.Int64, At: clockAt}
	}
	if r.whiteID != "" {
		r.White = &seat{AccountID: r.whiteID, Name: wName, StudentID: wStu, Rating: wRating}
	}
	if r.blackID != "" {
		r.Black = &seat{AccountID: r.blackID, Name: bName, StudentID: bStu, Rating: bRating}
	}
	// Whose move it is is in the position itself, so a list read can say so
	// without replaying every game.
	if r.Status == "Active" {
		if f := strings.Fields(r.FEN); len(f) > 1 {
			r.Turn = map[string]string{"w": "White", "b": "Black"}[f[1]]
		}
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
		// A move answers any draw offer standing: offering and then playing on
		// withdraws it, and replying with a move declines it.
		if applied.Result != "" {
			_, err = d.Exec(`UPDATE game_room SET fen = ?, status = 'Finished', draw_offer = NULL,
			                 result = ?, result_reason = ?, ended_at = datetime('now')
			                 WHERE game_room_id = ?`,
				applied.FEN, applied.Result, applied.Reason, room.ID)
		} else {
			_, err = d.Exec(`UPDATE game_room SET fen = ?, draw_offer = NULL WHERE game_room_id = ?`,
				applied.FEN, room.ID)
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

// handleDraw offers, accepts or declines a draw.
//
// One seat offers; only the other may accept or decline, and only while the
// offer stands. Accepting ends the game drawn "by agreement". Each step is a
// conditional UPDATE, so an offer withdrawn by a move cannot be accepted a
// moment later by a stale screen.
func handleDraw(d *sql.DB, h *hub, relay *lichessRelay, action string) http.HandlerFunc {
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
		opponent := "Black"
		if mySeat == "Black" {
			opponent = "White"
		}
		var res sql.Result
		switch action {
		case "offer":
			res, err = d.Exec(`UPDATE game_room SET draw_offer = ?
			                   WHERE game_room_id = ? AND status = 'Active' AND draw_offer IS NULL`,
				mySeat, room.ID)
		case "accept":
			res, err = d.Exec(`UPDATE game_room SET status = 'Finished', result = '1/2-1/2',
			                   result_reason = 'Agreement', draw_offer = NULL, ended_at = datetime('now')
			                   WHERE game_room_id = ? AND status = 'Active' AND draw_offer = ?`,
				room.ID, opponent)
		case "decline":
			res, err = d.Exec(`UPDATE game_room SET draw_offer = NULL
			                   WHERE game_room_id = ? AND status = 'Active' AND draw_offer = ?`,
				room.ID, opponent)
		}
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not record that", err)
			return
		}
		if n, _ := res.RowsAffected(); n == 0 {
			msg := "there is no draw offer to answer"
			if action == "offer" {
				msg = "a draw cannot be offered now"
			}
			httpx.Error(w, http.StatusConflict, msg, nil)
			return
		}
		publishRoom(d, h, room.ID)
		// A drawn board here has to be drawn on Lichess too, or the rated game
		// runs on until somebody's clock falls.
		if action == "accept" {
			relay.draw(room.ID, opponent)
		}
		httpx.JSON(w, http.StatusOK, map[string]string{"status": action})
	}
}

// handleStopRoom pauses a game in play so it can be finished another day.
//
// The game goes back to Open with every move kept; nobody can move until the
// office resumes it (handleResumeRoom) — the players cannot resume it
// themselves. Staff only, and only a game in play: a waiting one has nothing
// to hold and is removed instead.
//
// A rated game cannot be paused on Lichess, whose clock keeps running, so
// stopping one ends the Lichess side and the game carries on here unrated,
// saying why. The console warns before it does this.
func handleStopRoom(d *sql.DB, h *hub, relay *lichessRelay) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := requireIdentity(d, w, r)
		if id == nil {
			return
		}
		if !isStaff(id.Role) {
			httpx.Error(w, http.StatusForbidden, "not allowed", nil)
			return
		}
		roomID := r.PathValue("id")
		var rated int
		if err := d.QueryRow(`SELECT lichess_rated FROM game_room WHERE game_room_id = ? AND status = 'Active'`,
			roomID).Scan(&rated); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				httpx.Error(w, http.StatusConflict, "only a game in play can be stopped", nil)
				return
			}
			httpx.Error(w, http.StatusInternalServerError, "could not stop the game", err)
			return
		}
		res, err := d.Exec(`UPDATE game_room
		                    SET status = 'Open', stopped_at = datetime('now'), draw_offer = NULL
		                    WHERE game_room_id = ? AND status = 'Active'`, roomID)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not stop the game", err)
			return
		}
		if n, _ := res.RowsAffected(); n == 0 {
			httpx.Error(w, http.StatusConflict, "only a game in play can be stopped", nil)
			return
		}
		if rated == 1 {
			relay.abandon(roomID)
			if _, err := d.Exec(`UPDATE game_room SET lichess_rated = 0, lichess_detached_reason = 'stopped'
			                     WHERE game_room_id = ?`, roomID); err != nil {
				httpx.Error(w, http.StatusInternalServerError, "stopped, but could not record it as unrated", err)
				return
			}
		}
		publishRoom(d, h, roomID)
		httpx.JSON(w, http.StatusOK, map[string]string{"status": "stopped"})
	}
}

// handleResumeRoom lets a paused game carry on, from where it stopped.
// Staff only: the office decides when the two may finish it. It does not put
// the game straight back in play — the two may not be at the board yet — but
// back to waiting for both to press Enter, the way a game the office sets up
// starts. Moves, and the day it began, are kept.
func handleResumeRoom(d *sql.DB, h *hub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := requireIdentity(d, w, r)
		if id == nil {
			return
		}
		if !isStaff(id.Role) {
			httpx.Error(w, http.StatusForbidden, "not allowed", nil)
			return
		}
		roomID := r.PathValue("id")
		res, err := d.Exec(`UPDATE game_room
		                    SET stopped_at = NULL, white_entered_at = NULL, black_entered_at = NULL
		                    WHERE game_room_id = ? AND status = 'Open' AND stopped_at IS NOT NULL`, roomID)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not resume the game", err)
			return
		}
		if n, _ := res.RowsAffected(); n == 0 {
			httpx.Error(w, http.StatusConflict, "only a paused game can be resumed", nil)
			return
		}
		publishRoom(d, h, roomID)
		httpx.JSON(w, http.StatusOK, map[string]string{"status": "resumed"})
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

// handleDeleteRoom removes a room and its moves for good.
//
// Cancelling and deleting are different acts and both are wanted. Cancelling
// ends a game that is happening; the row stays, because a game that was played
// is a record. Deleting throws the record away, which is what the office needs
// for the rooms that are not records at all — a code minted for a lesson that
// did not happen, a board opened twice by mistake, the three test rooms from
// the afternoon somebody was learning the screen. Those accumulate at the top
// of the list forever and there was no way to be rid of one.
//
// So they are separate endpoints rather than one DELETE that changed meaning.
// An older console still sends DELETE for "Stop this game", and having that
// destroy a game mid-deploy is exactly the accident worth designing out.
//
// A game being played is refused. The two players are mid-move, and the fix
// for a game that should not be running is to stop it — which is the other
// endpoint, and reversible in a way this is not.
func handleDeleteRoom(d *sql.DB, h *hub, relay *lichessRelay) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := requireIdentity(d, w, r)
		if id == nil {
			return
		}
		if !isStaff(id.Role) {
			httpx.Error(w, http.StatusForbidden, "not allowed", nil)
			return
		}
		roomID := r.PathValue("id")

		var status string
		switch err := d.QueryRow(`SELECT status FROM game_room WHERE game_room_id = ?`,
			roomID).Scan(&status); {
		case errors.Is(err, sql.ErrNoRows):
			httpx.Error(w, http.StatusNotFound, "no such room", nil)
			return
		case err != nil:
			httpx.Error(w, http.StatusInternalServerError, "could not read room", err)
			return
		}
		if status == "Active" {
			httpx.Error(w, http.StatusConflict, "stop the game before deleting it", nil)
			return
		}

		gone, _ := loadRoom(d, roomID)
		tx, err := d.Begin()
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not delete room", err)
			return
		}
		defer tx.Rollback()

		// game_move.game_room_id is NOT NULL, so the moves go with the room
		// rather than being orphaned. A challenge's link is nullable and the
		// challenge itself is a separate record of who asked whom — that one is
		// unhooked, not destroyed.
		for _, step := range []struct {
			q    string
			args []any
		}{
			{`DELETE FROM game_move WHERE game_room_id = ?`, []any{roomID}},
			{`UPDATE game_challenge SET game_room_id = NULL WHERE game_room_id = ?`, []any{roomID}},
			{`DELETE FROM game_room WHERE game_room_id = ?`, []any{roomID}},
		} {
			if _, err := tx.Exec(step.q, step.args...); err != nil {
				httpx.Error(w, http.StatusInternalServerError, "could not delete room", err)
				return
			}
		}
		if err := tx.Commit(); err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not delete room", err)
			return
		}

		// An Open room can still hold a relay slot if it was minted rated, and
		// the watcher would outlive the row it is watching.
		relay.abandon(roomID)
		// Anyone with the board open learns it is over, rather than being left
		// looking at a position that no longer exists anywhere.
		// The last position goes with it, so the board stays drawn.
		if gone != nil {
			ev := roomEvent{RoomID: roomID, Status: "Cancelled", FEN: gone.FEN, Ply: gone.MoveCount,
				White: gone.White, Black: gone.Black, Finished: true, Removed: true}
			if payload, err := json.Marshal(ev); err == nil {
				h.broadcast(roomID, payload)
			}
		}
		httpx.JSON(w, http.StatusOK, map[string]string{"status": "deleted"})
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
