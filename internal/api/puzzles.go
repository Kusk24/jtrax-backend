// Puzzle endpoints: the daily set a pupil is given, and the grading of their
// attempts.
//
// Authoring is a registry resource (`puzzles`) restricted to staff and
// teachers, because it is ordinary CRUD. These two are not: choosing a set has
// to be stable for the day, and grading must never reveal the answer.
package api

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/Kusk24/jtrax-backend/internal/auth"
	"github.com/Kusk24/jtrax-backend/internal/httpx"
	"github.com/Kusk24/jtrax-backend/internal/puzzle"
)

// dailyCount is how many puzzles a pupil is set each day. Three is what the
// portal's daily-challenge card was built around.
const dailyCount = 3

// ratingBand is how far either side of a pupil's rating puzzles are drawn from.
// Wide enough that a small roster still fills a set, narrow enough that the
// puzzles are worth attempting.
const ratingBand = 250

// defaultRating is used for a pupil with no FIDE rating yet, which is most of
// them — a beginner-friendly floor rather than the middle of the distribution.
const defaultRating = 800

type puzzleView struct {
	PuzzleID string `json:"puzzleId"`
	FEN      string `json:"fen"`
	Rating   int    `json:"rating"`
	Themes   string `json:"themes"`
	// Which colour the pupil plays, so the board can be oriented for them.
	Side string `json:"side"`
	// How many of their own moves the solution needs, so the UI can say
	// "mate in 2" without being told which moves they are.
	MoveCount int  `json:"moveCount"`
	Solved    bool `json:"solved"`
	Wrong     int  `json:"wrongMoves"`
	// Deliberately absent: `moves`. See the package comment.
}

// studentOf resolves the caller's student row, or writes the error itself.
func studentOf(w http.ResponseWriter, id *auth.Identity) (string, bool) {
	if id.Role != "Student" || id.StudentID == "" {
		httpx.Error(w, http.StatusForbidden, "only students are set puzzles", nil)
		return "", false
	}
	return id.StudentID, true
}

func today() string { return time.Now().Format("2006-01-02") }

// handleDailyPuzzles returns the pupil's set for today, creating it on first
// request.
//
// The set is materialised as puzzle_attempt rows rather than recomputed, which
// is what makes it stable: refreshing the page cannot reroll a puzzle the pupil
// has just failed.
func handleDailyPuzzles(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := requireIdentity(d, w, r)
		if id == nil {
			return
		}
		studentID, ok := studentOf(w, id)
		if !ok {
			return
		}
		day := today()

		if err := assignDaily(d, studentID, day); err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not prepare today's puzzles", err)
			return
		}
		rows, err := d.Query(`
			SELECT p.puzzle_id, p.fen, p.rating, p.themes, p.moves, a.solved, a.wrong_moves
			FROM puzzle_attempt a JOIN puzzle p ON p.puzzle_id = a.puzzle_id
			WHERE a.student_id = ? AND a.assigned_on = ?
			ORDER BY p.rating, p.puzzle_id`, studentID, day)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "query failed", err)
			return
		}
		defer rows.Close()

		out := []puzzleView{}
		for rows.Next() {
			var v puzzleView
			var moves string
			var solved, wrong int
			if err := rows.Scan(&v.PuzzleID, &v.FEN, &v.Rating, &v.Themes, &moves, &solved, &wrong); err != nil {
				httpx.Error(w, http.StatusInternalServerError, "query failed", err)
				return
			}
			// `moves` is read only to derive these two; it never leaves here.
			v.MoveCount = (len(strings.Fields(moves)) + 1) / 2
			v.Side, _ = puzzle.SideToMove(v.FEN)
			v.Solved = solved == 1
			v.Wrong = wrong
			out = append(out, v)
		}
		httpx.JSON(w, http.StatusOK, out)
	}
}

// assignDaily fills today's set if it is not already there.
func assignDaily(d *sql.DB, studentID, day string) error {
	var have int
	if err := d.QueryRow(`SELECT COUNT(*) FROM puzzle_attempt WHERE student_id = ? AND assigned_on = ?`,
		studentID, day).Scan(&have); err != nil {
		return err
	}
	if have >= dailyCount {
		return nil
	}

	var rating sql.NullFloat64
	if err := d.QueryRow(`SELECT fide_rating FROM student WHERE student_id = ?`, studentID).Scan(&rating); err != nil {
		return err
	}
	target := defaultRating
	if rating.Valid && rating.Float64 > 0 {
		target = int(rating.Float64)
	}

	// Prefer puzzles near the pupil's rating that they have never been set
	// before; fall back to anything unseen, then to anything at all, so a small
	// puzzle table still fills a set rather than returning a short day.
	rows, err := d.Query(`
		SELECT puzzle_id FROM puzzle
		WHERE puzzle_id NOT IN (SELECT puzzle_id FROM puzzle_attempt WHERE student_id = ?)
		ORDER BY ABS(rating - ?) LIMIT ?`,
		studentID, target, dailyCount-have)
	if err != nil {
		return err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var pid string
		if err := rows.Scan(&pid); err != nil {
			return err
		}
		ids = append(ids, pid)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, pid := range ids {
		// A second request racing the first would violate the unique key rather
		// than double-assign, so the error is expected and ignored.
		d.Exec(`INSERT INTO puzzle_attempt (puzzle_attempt_id, student_id, puzzle_id, assigned_on)
		        VALUES (?, ?, ?, ?)`, newID("pza"), studentID, pid, day)
	}
	return nil
}

// handlePuzzleAttempt grades one move.
//
// The client sends only its own moves so far; the opponent's replies come from
// the stored solution, so the server rebuilds the position rather than trusting
// the client to report it.
func handlePuzzleAttempt(d *sql.DB) http.HandlerFunc {
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
			Played []string `json:"played"`
			Move   string   `json:"move"`
		}
		if err := httpx.Decode(r, &in); err != nil || strings.TrimSpace(in.Move) == "" {
			httpx.Error(w, http.StatusBadRequest, "a move is required", err)
			return
		}
		if len(in.Move) > 6 || len(in.Played) > 32 {
			httpx.Error(w, http.StatusBadRequest, "that is not a move", nil)
			return
		}

		puzzleID := r.PathValue("id")
		var fen, solution string
		// Joined against the assignment so a pupil can only submit against a
		// puzzle they were actually set — otherwise the endpoint is a way to
		// grind through the whole table.
		err := d.QueryRow(`
			SELECT p.fen, p.moves FROM puzzle p
			JOIN puzzle_attempt a ON a.puzzle_id = p.puzzle_id
			WHERE p.puzzle_id = ? AND a.student_id = ? AND a.assigned_on = ?`,
			puzzleID, studentID, today()).Scan(&fen, &solution)
		if err != nil {
			httpx.Error(w, http.StatusNotFound, "that puzzle is not in today's set", nil)
			return
		}

		verdict, err := puzzle.Grade(fen, solution, in.Played, in.Move)
		if err != nil {
			if errors.Is(err, puzzle.ErrBadPuzzle) {
				httpx.Error(w, http.StatusConflict, "that puzzle could not be graded", err)
				return
			}
			httpx.Error(w, http.StatusInternalServerError, "could not grade the attempt", err)
			return
		}

		if verdict.Correct && verdict.Solved {
			d.Exec(`UPDATE puzzle_attempt SET solved = 1, solved_at = datetime('now')
			        WHERE student_id = ? AND puzzle_id = ? AND assigned_on = ?`,
				studentID, puzzleID, today())
		} else if !verdict.Correct {
			d.Exec(`UPDATE puzzle_attempt SET wrong_moves = wrong_moves + 1
			        WHERE student_id = ? AND puzzle_id = ? AND assigned_on = ?`,
				studentID, puzzleID, today())
		}

		httpx.JSON(w, http.StatusOK, map[string]any{
			"correct": verdict.Correct,
			"solved":  verdict.Solved,
			"reply":   verdict.Reply,
			"fen":     verdict.FEN,
		})
	}
}

func mountPuzzles(mux *http.ServeMux, d *sql.DB) {
	mux.HandleFunc("GET /api/v1/puzzles/daily", handleDailyPuzzles(d))
	mux.HandleFunc("POST /api/v1/puzzles/{id}/attempt", handlePuzzleAttempt(d))
}

// puzzleSources are the two ways a puzzle gets here: bulk-imported from the
// Lichess database, or authored by a teacher.
var puzzleSources = []string{"Lichess", "JCA"}

// checkPuzzle rejects a puzzle whose solution does not play out from its
// position. Without it a teacher's typo becomes a pupil stuck on an unsolvable
// board, and the mistake surfaces days later as "the app is broken".
func checkPuzzle(row map[string]any) error {
	fen, _ := row["fen"].(string)
	moves, _ := row["moves"].(string)
	if err := puzzle.Validate(fen, moves); err != nil {
		return errors.New("the solution does not play from that position")
	}
	return nil
}
