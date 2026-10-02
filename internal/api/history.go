// A pupil's chess at the academy, kept for as long as they are on the roster.
//
// Three things make up that history and they are recorded in three places:
// games against the computer (solo_game, written here), games on the academy's
// own board including the ones relayed to lichess.org (game_room), and puzzles
// (puzzle_attempt). This file records the first and reads all three as one
// timeline.
//
// Games played on lichess.org *outside* JTrax are deliberately not here. The
// sync keeps a rating per day, which is the progress a coach reads; importing
// a child's every blitz game is a different feature and a much larger one.
package api

import (
	"database/sql"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Kusk24/jtrax-backend/internal/academytime"
	"github.com/Kusk24/jtrax-backend/internal/auth"
	"github.com/Kusk24/jtrax-backend/internal/httpx"
)

// soloOpponents are the three trained opponents the portal offers. Validated
// so the history cannot fill up with names nothing will ever render.
var soloOpponents = map[string]bool{"novice": true, "strong": true, "expert": true}

// maxSoloMoves bounds one stored game. A real game is nowhere near this; the
// cap is here because the move list arrives from a browser.
const maxSoloMoves = 600

// canSeeStudent reports whether the caller may read this pupil's record:
// the pupil themselves, a parent of theirs, or staff and teachers.
//
// Decided in a query rather than from the role alone, so a parent of one child
// cannot read another's by changing the id in the URL.
func canSeeStudent(d *sql.DB, id *auth.Identity, studentID string) bool {
	if studentID == "" {
		return false
	}
	switch {
	case id.Role == "Student":
		return id.StudentID == studentID
	case isStaff(id.Role) || id.Role == "Teacher":
		var n int
		_ = d.QueryRow(`SELECT COUNT(*) FROM student WHERE student_id = ?`, studentID).Scan(&n)
		return n > 0
	case id.Role == "Parent":
		var n int
		_ = d.QueryRow(`SELECT COUNT(*) FROM student_parent WHERE parent_id = ? AND student_id = ?`,
			id.ParentID, studentID).Scan(&n)
		return n > 0
	}
	return false
}

// handleRecordSoloGame stores a finished game against the computer.
//
// The moves come from the browser because the browser is where the game was
// played — there is no server-side opponent to have watched it. That makes
// this a record of practice rather than a result the academy stands behind,
// which is also how it is shown: a pupil's own history, not a league table.
func handleRecordSoloGame(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := requireIdentity(d, w, r)
		if id == nil {
			return
		}
		studentID, ok := studentOf(w, id)
		if !ok {
			return
		}
		var in struct {
			Opponent  string   `json:"opponent"`
			Moves     []string `json:"moves"`
			Result    string   `json:"result"`
			Reason    string   `json:"reason"`
			StartedAt string   `json:"startedAt"`
		}
		if err := httpx.Decode(r, &in); err != nil {
			httpx.Error(w, http.StatusBadRequest, "invalid body", err)
			return
		}
		if !soloOpponents[in.Opponent] {
			httpx.Error(w, http.StatusUnprocessableEntity, "unknown opponent", nil)
			return
		}
		if len(in.Moves) == 0 {
			// A board opened and abandoned is not a game. Recording it would
			// fill a child's history with things they did not play.
			httpx.Error(w, http.StatusUnprocessableEntity, "a game needs at least one move", nil)
			return
		}
		if len(in.Moves) > maxSoloMoves {
			httpx.Error(w, http.StatusUnprocessableEntity, "too many moves", nil)
			return
		}
		switch in.Result {
		case "1-0", "0-1", "1/2-1/2", "":
		default:
			httpx.Error(w, http.StatusUnprocessableEntity, "unknown result", nil)
			return
		}

		var result any
		if in.Result != "" {
			result = in.Result
		}
		var started any
		if in.StartedAt != "" {
			started = in.StartedAt
		}
		if _, err := d.Exec(`
			INSERT INTO solo_game (solo_game_id, student_id, opponent, student_side,
			                       moves, move_count, result, result_reason, started_at)
			VALUES (?,?,?,'white',?,?,?,?,?)`,
			newID("slo"), studentID, in.Opponent,
			strings.Join(in.Moves, " "), len(in.Moves), result, nullable(in.Reason), started); err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not record the game", err)
			return
		}
		httpx.JSON(w, http.StatusCreated, map[string]any{"recorded": true})
	}
}

// historyEntry is one thing a pupil did, whatever kind it was.
type historyEntry struct {
	// "solo", "room" or "puzzle".
	Kind string `json:"kind"`
	ID   string `json:"id"`
	At   string `json:"at"`
	// Who or what they played: an opponent's name, a puzzle's rating.
	Against string `json:"against"`
	Result  string `json:"result,omitempty"`
	Reason  string `json:"reason,omitempty"`
	Moves   int    `json:"moves,omitempty"`
	// The academy's calendar day it happened on. At is a UTC timestamp for
	// games, so a game at 6am Bangkok would otherwise land on yesterday.
	Day string `json:"day"`
	// For a game: when it began, so the console can say how long it took.
	// For a puzzle: when it was opened.
	StartedAt string `json:"startedAt,omitempty"`
	// For a puzzle: "daily" (the day's set) or "free" (free play).
	Source string `json:"source,omitempty"`
	// Set on a board game that was also a real game on lichess.org, with the
	// id to open it there. This is the only kind of play that counts on a
	// pupil's Lichess rating.
	LichessGameID string `json:"lichessGameId,omitempty"`

	// For a game, what the student portal's History shows about it.
	// Opponent is the other player's name, or the computer opponent's key.
	Opponent string `json:"opponent,omitempty"`
	// "white" or "black": the pupil's side.
	Side string `json:"side,omitempty"`
	// "5+0" when a clock was chosen; empty for an untimed game.
	TimeControl string `json:"timeControl,omitempty"`
	// "computer", "class" (the office handed it out) or "challenge" (one
	// pupil invited another).
	GameType string `json:"gameType,omitempty"`
	// What it earned towards the student portal's points: a finished game is
	// worth the same whoever won, a puzzle only when solved.
	Points int `json:"points,omitempty"`
}

// clockLabel is a time control as the portal shows it, "10+5".
func clockLabel(limit, increment int) string {
	return strconv.Itoa(limit/60) + "+" + strconv.Itoa(increment)
}

// handleStudentHistory returns the pupil's chess, newest first.
func handleStudentHistory(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := requireIdentity(d, w, r)
		if id == nil {
			return
		}
		studentID := r.PathValue("studentId")
		if !canSeeStudent(d, id, studentID) {
			// Indistinguishable from a pupil who does not exist, so the URL
			// cannot be used to discover ids.
			httpx.Error(w, http.StatusNotFound, "not found", nil)
			return
		}

		out := []historyEntry{}

		// Against the computer.
		rows, err := d.Query(`SELECT solo_game_id, ended_at, opponent,
		                             COALESCE(result,''), COALESCE(result_reason,''), move_count,
		                             COALESCE(started_at,''), student_side
		                      FROM solo_game WHERE student_id = ?
		                      ORDER BY ended_at DESC LIMIT 200`, studentID)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not load history", err)
			return
		}
		for rows.Next() {
			e := historyEntry{Kind: "solo"}
			if err := rows.Scan(&e.ID, &e.At, &e.Against, &e.Result, &e.Reason, &e.Moves, &e.StartedAt, &e.Side); err == nil {
				e.Day = academyDayOf(e.At)
				e.Opponent, e.GameType = e.Against, "computer"
				if e.Result != "" {
					e.Points = pointsGame
				}
				out = append(out, e)
			}
		}
		rows.Close()

		// On the academy's board. The pupil's account is one of the two seats;
		// a room they only watched is not their game.
		rows, err = d.Query(`SELECT g.game_room_id, COALESCE(g.ended_at, g.created_at),
		                            COALESCE(g.label,''), COALESCE(g.result,''),
		                            COALESCE(g.result_reason,''), COALESCE(g.lichess_game_id,''),
		                            (SELECT COUNT(*) FROM game_move m WHERE m.game_room_id = g.game_room_id),
		                            COALESCE(g.started_at,''),
		                            CASE WHEN g.white_account_id = s.user_account_id THEN 'white' ELSE 'black' END,
		                            COALESCE((SELECT COALESCE(os.name, ou.display_name) FROM user_account ou
		                                        LEFT JOIN student os ON os.user_account_id = ou.user_account_id
		                                       WHERE ou.user_account_id = CASE WHEN g.white_account_id = s.user_account_id
		                                                                       THEN g.black_account_id ELSE g.white_account_id END), ''),
		                            g.timed, g.lichess_clock_limit, g.lichess_clock_increment,
		                            EXISTS(SELECT 1 FROM game_challenge c WHERE c.game_room_id = g.game_room_id)
		                     FROM game_room g
		                     JOIN student s ON s.user_account_id IN (g.white_account_id, g.black_account_id)
		                     WHERE s.student_id = ? AND g.status IN ('Finished','Active')
		                     ORDER BY COALESCE(g.ended_at, g.created_at) DESC LIMIT 200`, studentID)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not load history", err)
			return
		}
		for rows.Next() {
			e := historyEntry{Kind: "room"}
			var timed, limit, increment int
			var fromChallenge bool
			if err := rows.Scan(&e.ID, &e.At, &e.Against, &e.Result, &e.Reason, &e.LichessGameID, &e.Moves, &e.StartedAt,
				&e.Side, &e.Opponent, &timed, &limit, &increment, &fromChallenge); err == nil {
				e.Day = academyDayOf(e.At)
				e.GameType = "class"
				if fromChallenge {
					e.GameType = "challenge"
				}
				if timed == 1 {
					e.TimeControl = clockLabel(limit, increment)
				}
				if e.Result != "" {
					e.Points = pointsGame
				}
				out = append(out, e)
			}
		}
		rows.Close()

		// Puzzles, which are already recorded and were simply never shown.
		rows, err = d.Query(`SELECT a.puzzle_id, COALESCE(a.solved_at, a.assigned_on),
		                            p.rating, a.solved, a.wrong_moves, a.assigned_on,
		                            COALESCE(a.opened_at,''), a.source
		                     FROM puzzle_attempt a JOIN puzzle p ON p.puzzle_id = a.puzzle_id
		                     WHERE a.student_id = ?
		                     ORDER BY COALESCE(a.solved_at, a.assigned_on) DESC LIMIT 200`, studentID)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not load history", err)
			return
		}
		for rows.Next() {
			var rating, solved, wrong int
			e := historyEntry{Kind: "puzzle"}
			// assigned_on is already the academy's day.
			if err := rows.Scan(&e.ID, &e.At, &rating, &solved, &wrong, &e.Day, &e.StartedAt, &e.Source); err == nil {
				e.Against = strconv.Itoa(rating)
				e.Moves = wrong
				if solved == 1 {
					e.Result = "solved"
					e.Points = pointsPuzzle
				} else {
					e.Result = "unsolved"
				}
				out = append(out, e)
			}
		}
		rows.Close()

		sortHistory(out)
		httpx.JSON(w, http.StatusOK, map[string]any{"history": out})
	}
}

// academyDayOf is the academy's calendar day for a timestamp SQLite wrote
// with datetime('now'), which is UTC. Anything else passes through by its
// leading date.
func academyDayOf(stamp string) string {
	if t, err := time.ParseInLocation(sqliteTimeLayout, stamp, time.UTC); err == nil {
		return t.In(academytime.Now().Location()).Format(academytime.DayLayout)
	}
	if len(stamp) >= 10 {
		return stamp[:10]
	}
	return stamp
}

// sortHistory puts the whole timeline in one order, newest first. The three
// reads are merged here rather than in SQL: they have different shapes and a
// UNION would need every column padded to match.
func sortHistory(all []historyEntry) {
	sort.SliceStable(all, func(i, j int) bool { return all[i].At > all[j].At })
}

// handleGetSoloGame returns one game against the computer, with its moves,
// so the pupil can replay it from History. Readable by whoever can read the
// pupil's history; anyone else gets the same 404 as a game that is not there.
func handleGetSoloGame(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := requireIdentity(d, w, r)
		if id == nil {
			return
		}
		var studentID, opponent, side, moves, result, reason, ended string
		err := d.QueryRow(`SELECT student_id, opponent, student_side, moves,
		                          COALESCE(result,''), COALESCE(result_reason,''), ended_at
		                     FROM solo_game WHERE solo_game_id = ?`, r.PathValue("id")).
			Scan(&studentID, &opponent, &side, &moves, &result, &reason, &ended)
		if err != nil || !canSeeStudent(d, id, studentID) {
			httpx.Error(w, http.StatusNotFound, "not found", nil)
			return
		}
		list := []string{}
		if strings.TrimSpace(moves) != "" {
			list = strings.Fields(moves)
		}
		httpx.JSON(w, http.StatusOK, map[string]any{
			"id": r.PathValue("id"), "opponent": opponent, "side": side, "moves": list,
			"result": result, "reason": reason, "at": ended,
		})
	}
}

func mountHistory(mux *http.ServeMux, d *sql.DB) {
	mux.HandleFunc("POST /api/v1/games/solo", handleRecordSoloGame(d))
	mux.HandleFunc("GET /api/v1/games/solo/{id}", handleGetSoloGame(d))
	mux.HandleFunc("GET /api/v1/students/{studentId}/history", handleStudentHistory(d))
}
