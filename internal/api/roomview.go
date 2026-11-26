// Game room storage and the views returned to players and spectators.
package api

import (
	"database/sql"
	"strings"

	"github.com/Kusk24/jtrax-backend/internal/auth"
)

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
