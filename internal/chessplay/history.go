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
package chessplay

import (
	"database/sql"
	"net/http"
	"strings"

	"github.com/Kusk24/jtrax-backend/internal/accounts"
	"github.com/Kusk24/jtrax-backend/internal/db"
	"github.com/Kusk24/jtrax-backend/internal/httpx"
	"github.com/Kusk24/jtrax-backend/internal/studenthistory"
)

// soloOpponents are the three trained opponents the portal offers. Validated
// so the history cannot fill up with names nothing will ever render.
var soloOpponents = map[string]bool{"novice": true, "strong": true, "expert": true}

// maxSoloMoves bounds one stored game. A real game is nowhere near this; the
// cap is here because the move list arrives from a browser.
const maxSoloMoves = 600

// handleRecordSoloGame stores a finished game against the computer.
//
// The moves come from the browser because the browser is where the game was
// played — there is no server-side opponent to have watched it. That makes
// this a record of practice rather than a result the academy stands behind,
// which is also how it is shown: a pupil's own history, not a league table.
func handleRecordSoloGame(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := accounts.RequireIdentity(d, w, r)
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
			db.NewID("slo"), studentID, in.Opponent,
			strings.Join(in.Moves, " "), len(in.Moves), result, db.Nullable(in.Reason), started); err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not record the game", err)
			return
		}
		httpx.JSON(w, http.StatusCreated, map[string]any{"recorded": true})
	}
}

// handleStudentHistory returns the pupil's chess, newest first.
func handleStudentHistory(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := accounts.RequireIdentity(d, w, r)
		if id == nil {
			return
		}
		studentID := r.PathValue("studentId")
		if !accounts.CanSeeStudent(d, id, studentID) {
			// Indistinguishable from a pupil who does not exist, so the URL
			// cannot be used to discover ids.
			httpx.Error(w, http.StatusNotFound, "not found", nil)
			return
		}

		out, err := studenthistory.Load(d, studentID)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not load history", err)
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"history": out})
	}
}

// handleGetSoloGame returns one game against the computer, with its moves,
// so the pupil can replay it from History. Readable by whoever can read the
// pupil's history; anyone else gets the same 404 as a game that is not there.
func handleGetSoloGame(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := accounts.RequireIdentity(d, w, r)
		if id == nil {
			return
		}
		var studentID, opponent, side, moves, result, reason, ended string
		err := d.QueryRow(`SELECT student_id, opponent, student_side, moves,
		                          COALESCE(result,''), COALESCE(result_reason,''), ended_at
		                     FROM solo_game WHERE solo_game_id = ?`, r.PathValue("id")).
			Scan(&studentID, &opponent, &side, &moves, &result, &reason, &ended)
		if err != nil || !accounts.CanSeeStudent(d, id, studentID) {
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

func mountHistory(mux httpx.RouteRegistrar, d *sql.DB) {
	mux.HandleFunc("POST /api/v1/games/solo", handleRecordSoloGame(d))
	mux.HandleFunc("GET /api/v1/games/solo/{id}", handleGetSoloGame(d))
	mux.HandleFunc("GET /api/v1/students/{studentId}/history", handleStudentHistory(d))
}
