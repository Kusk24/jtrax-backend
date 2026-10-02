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
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/Kusk24/jtrax-backend/internal/academytime"
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

// pointsPerPuzzle is the score a solved puzzle is worth on the practice record.
const pointsPerPuzzle = 10

// maxPuzzleMinutes caps what one puzzle can contribute to a day's practice
// time. A child who opens a puzzle and goes to dinner should not come back
// having "practised" for three hours, and the tab left open overnight is the
// ordinary case rather than the strange one.
const maxPuzzleMinutes = 15

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

// today is the academy's calendar day (see internal/academytime), not the
// server's: on a host set to UTC the two disagree for the first seven hours of
// every Bangkok morning. Dates in this schema are plain YYYY-MM-DD with no
// zone, so a deadline or a daily set compares against this as a string, and
// the boundary is Bangkok midnight — the same day the poster says.
func today() string { return academytime.Today() }

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
			WHERE a.student_id = ? AND a.assigned_on = ? AND a.source = 'daily'
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

		// How many the pupil has never been set. Zero with a short set means the
		// bank is spent for them, which the portal has to be able to say — a
		// silently empty daily challenge reads as a broken app, and with sixty
		// seeded puzzles this arrives on about the twentieth day.
		var unseen int
		if err := d.QueryRow(`SELECT COUNT(*) FROM puzzle
		                      WHERE puzzle_id NOT IN
		                        (SELECT puzzle_id FROM puzzle_attempt WHERE student_id = ?)`,
			studentID).Scan(&unseen); err != nil {
			httpx.Error(w, http.StatusInternalServerError, "query failed", err)
			return
		}

		httpx.JSON(w, http.StatusOK, map[string]any{
			"puzzles": out,
			// True only when there is nothing left to give, not merely when
			// today's set is short for some other reason.
			"exhausted": len(out) < dailyCount && unseen == 0,
			"unseen":    unseen,
		})
	}
}

// dailyTarget is the rating a pupil's daily puzzles aim at, and the band they
// are drawn from first. The band is the pupil's level as the office set it —
// the same bands Practice uses — so an Advanced pupil is not handed 800-rated
// puzzles because they have no FIDE rating. A FIDE rating inside that band
// sharpens the aim; with no level on file the FIDE rating (or 800) decides.
func dailyTarget(d *sql.DB, studentID string) (int, tier, error) {
	var level sql.NullString
	var rating sql.NullFloat64
	if err := d.QueryRow(`SELECT current_level, fide_rating FROM student WHERE student_id = ?`, studentID).Scan(&level, &rating); err != nil {
		return 0, tier{}, err
	}
	fide := 0
	if rating.Valid && rating.Float64 > 0 {
		fide = int(rating.Float64)
	}
	if t, ok := tiers[strings.ToLower(strings.TrimSpace(level.String))]; ok {
		if fide >= t.minRate && fide <= t.maxRate {
			return fide, t, nil
		}
		return (t.minRate + min(t.maxRate, 1600)) / 2, t, nil
	}
	target := defaultRating
	if fide > 0 {
		target = fide
	}
	return target, tier{minRate: target - ratingBand, maxRate: target + ratingBand}, nil
}

// assignDaily fills today's set if it is not already there.
//
// Puzzles in today's set the pupil has not touched (not opened, not tried,
// not solved) are swapped out when they fall outside the pupil's level — so a
// level the office changes today takes effect today, not tomorrow.
func assignDaily(d *sql.DB, studentID, day string) error {
	if _, band, err := dailyTarget(d, studentID); err == nil {
		d.Exec(`DELETE FROM puzzle_attempt
		        WHERE student_id = ? AND assigned_on = ? AND source = 'daily'
		          AND solved = 0 AND wrong_moves = 0 AND opened_at IS NULL
		          AND puzzle_id IN (SELECT puzzle_id FROM puzzle WHERE rating NOT BETWEEN ? AND ?)
		          AND EXISTS (SELECT 1 FROM puzzle p WHERE p.rating BETWEEN ? AND ?
		                        AND p.puzzle_id NOT IN (SELECT puzzle_id FROM puzzle_attempt WHERE student_id = ?)
		                        AND p.puzzle_id NOT IN (SELECT puzzle_id FROM puzzle_list WHERE student_id = ?))`,
			studentID, day, band.minRate, band.maxRate, band.minRate, band.maxRate, studentID, studentID)
	}
	var have int
	if err := d.QueryRow(`SELECT COUNT(*) FROM puzzle_attempt
	                      WHERE student_id = ? AND assigned_on = ? AND source = 'daily'`,
		studentID, day).Scan(&have); err != nil {
		return err
	}
	if have >= dailyCount {
		return nil
	}

	target, band, err := dailyTarget(d, studentID)
	if err != nil {
		return err
	}

	// Never a puzzle this pupil has been set before — repeating one they have
	// already solved is worth nothing, and repeating one they failed teaches
	// them the answer rather than the idea.
	//
	// Within the band first, so a beginner is not handed a club player's fork
	// merely because the bank is thin there; then nearest by rating, so the
	// set still fills when the band is empty. `ratingBand` was declared for
	// this and then never used — the query ordered by distance alone, which
	// silently widened to the whole table.
	rows, err := d.Query(`
		SELECT puzzle_id FROM puzzle
		WHERE puzzle_id NOT IN (SELECT puzzle_id FROM puzzle_attempt WHERE student_id = ?)
		  AND puzzle_id NOT IN (SELECT puzzle_id FROM puzzle_list WHERE student_id = ?)
		ORDER BY (rating NOT BETWEEN ? AND ?), (ABS(rating - ?) > ?), ABS(rating - ?) LIMIT ?`,
		studentID, studentID, band.minRate, band.maxRate, target, ratingBand, target, dailyCount-have)
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

			// The practice record is written here, from what the server just
			// graded, rather than posted by the browser afterwards. The portal
			// used to send its own `streak_count`, which made a child's flame a
			// number their device chose.
			var solvedToday int
			if err := d.QueryRow(`SELECT COALESCE(SUM(solved),0) FROM puzzle_attempt
			                      WHERE student_id = ? AND assigned_on = ?`,
				studentID, today()).Scan(&solvedToday); err == nil {
				// Measured, not guessed: the time this puzzle was open, added
				// to what the day already had. The browser used to post a flat
				// ten minutes whatever happened.
				if err := addPracticeMinutes(d, studentID, today(), solvedToday,
					minutesOnPuzzle(d, studentID, puzzleID), solvedToday*pointsPerPuzzle); err != nil {
					log.Printf("puzzles: recording practice for %s: %v", studentID, err)
				}
			}
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

// handlePuzzleOpen stamps when a pupil started looking at a puzzle.
//
// The server writes its own clock, so the pupil supplies the moment but not
// the time. Only the first open counts: coming back to a puzzle after a wrong
// answer continues the same sitting rather than starting a new one.
func handlePuzzleOpen(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := requireIdentity(d, w, r)
		if id == nil {
			return
		}
		studentID, ok := studentOf(w, id)
		if !ok {
			return
		}
		// Scoped to today's set, so this cannot stamp a puzzle the pupil was
		// never given.
		res, err := d.Exec(`UPDATE puzzle_attempt SET opened_at = datetime('now')
		                    WHERE student_id = ? AND puzzle_id = ? AND assigned_on = ?
		                      AND opened_at IS NULL`,
			studentID, r.PathValue("id"), today())
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not record", err)
			return
		}
		n, _ := res.RowsAffected()
		httpx.JSON(w, http.StatusOK, map[string]any{"started": n > 0})
	}
}

// minutesOnPuzzle is how long the pupil had this puzzle open, in whole
// minutes, capped. Zero when it was never opened through the app.
func minutesOnPuzzle(d *sql.DB, studentID, puzzleID string) int {
	var opened sql.NullString
	if err := d.QueryRow(`SELECT opened_at FROM puzzle_attempt
	                      WHERE student_id = ? AND puzzle_id = ? AND assigned_on = ?`,
		studentID, puzzleID, today()).Scan(&opened); err != nil || !opened.Valid || opened.String == "" {
		return 0
	}
	start, err := time.Parse(sqliteTimeLayout, opened.String)
	if err != nil {
		return 0
	}
	mins := int(time.Since(start).Minutes())
	if mins < 0 {
		return 0
	}
	if mins > maxPuzzleMinutes {
		return maxPuzzleMinutes
	}
	return mins
}

func mountPuzzles(mux *http.ServeMux, d *sql.DB) {
	mux.HandleFunc("GET /api/v1/puzzles/daily", handleDailyPuzzles(d))
	mux.HandleFunc("POST /api/v1/puzzles/{id}/open", handlePuzzleOpen(d))
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
