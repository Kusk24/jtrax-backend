// The practice list: twenty puzzles per pupil, levels mixed.
//
// Two in three come from the pupil's own level (the level the office set, as
// the daily set uses); the other third is split between the other two levels,
// the nearer one taking more. A solved puzzle is ticked and stays playable for
// the rest of the day. The next day it is replaced by a new one of the same
// level, while unsolved puzzles stay where they are — so the list is never
// reshuffled under a child halfway through it.
//
// Only a first solve counts: it is written to puzzle_attempt as a free-play
// solve, so points, history and the dashboard read it with no special case,
// and the puzzle is never handed out again.
package api

import (
	"database/sql"
	"errors"
	"hash/fnv"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/Kusk24/jtrax-backend/internal/httpx"
	"github.com/Kusk24/jtrax-backend/internal/lichess"
	"github.com/Kusk24/jtrax-backend/internal/puzzle"
)

// listSize is how many puzzles the list holds.
const listSize = 20

// listTopUps caps the Lichess calls one request may make while filling the
// list: a child opening a page must not become an unbounded loop against
// someone else's API.
const listTopUps = 12

// tierOrder is the levels from easiest; distance in it decides which of the
// other two levels is "nearer".
var tierOrder = []string{"beginner", "intermediate", "advanced"}

// listMix is how many of the twenty each level gets for a pupil at `own`:
// two in three from their own level, the rest split with the nearer level
// taking the larger share.
func listMix(own string) map[string]int {
	ownIdx := 1
	for i, name := range tierOrder {
		if name == own {
			ownIdx = i
		}
	}
	mix := map[string]int{own: 13}
	others := []int{}
	for i := range tierOrder {
		if i != ownIdx {
			others = append(others, i)
		}
	}
	// Nearer first; for Intermediate both are one away and the easier one leads.
	sort.SliceStable(others, func(a, b int) bool {
		da, db := abs(others[a]-ownIdx), abs(others[b]-ownIdx)
		if da != db {
			return da < db
		}
		return others[a] < others[b]
	})
	mix[tierOrder[others[0]]] = 4
	mix[tierOrder[others[1]]] = 3
	return mix
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// pupilTier is the level the list is built around: the office's level, else
// the band the pupil's FIDE rating (or the 800 default) falls in.
func pupilTier(d *sql.DB, studentID string) (string, error) {
	target, band, err := dailyTarget(d, studentID)
	if err != nil {
		return "", err
	}
	if band.name != "" {
		return band.name, nil
	}
	for _, name := range tierOrder {
		if t := tiers[name]; target >= t.minRate && target <= t.maxRate {
			return name, nil
		}
	}
	return "intermediate", nil
}

// candidates are unseen puzzles in a tier's band, in a stable order.
func candidates(d *sql.DB, studentID string, t tier, limit int) ([]string, error) {
	rows, err := d.Query(`
		SELECT puzzle_id FROM puzzle
		WHERE rating BETWEEN ? AND ?
		  AND puzzle_id NOT IN (SELECT puzzle_id FROM puzzle_attempt WHERE student_id = ?)
		  AND puzzle_id NOT IN (SELECT puzzle_id FROM puzzle_list WHERE student_id = ?)
		ORDER BY rating, puzzle_id LIMIT ?`, t.minRate, t.maxRate, studentID, studentID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// refreshList drops puzzles solved before today and fills the list back up
// to twenty in the pupil's mix, topping the bank up from Lichess when a level
// runs dry and borrowing from other levels only when all else fails.
func refreshList(d *sql.DB, lc *lichess.Client, studentID string) error {
	day := today()
	if _, err := d.Exec(`DELETE FROM puzzle_list WHERE student_id = ? AND solved_on IS NOT NULL AND solved_on < ?`,
		studentID, day); err != nil {
		return err
	}
	own, err := pupilTier(d, studentID)
	if err != nil {
		return err
	}
	have := map[string]int{}
	used := map[int]bool{}
	rows, err := d.Query(`SELECT tier, position FROM puzzle_list WHERE student_id = ?`, studentID)
	if err != nil {
		return err
	}
	total := 0
	for rows.Next() {
		var tierName string
		var pos int
		if err := rows.Scan(&tierName, &pos); err != nil {
			rows.Close()
			return err
		}
		have[tierName]++
		used[pos] = true
		total++
	}
	rows.Close()
	if total >= listSize {
		return nil
	}

	type pick struct{ id, tier string }
	picks := []pick{}
	topUps := 0
	take := func(name string, want int) error {
		for want > 0 {
			// The picks so far are not in puzzle_list yet, so the query still
			// returns them; ask for enough to see past them.
			ids, err := candidates(d, studentID, tiers[name], want+len(picks))
			if err != nil {
				return err
			}
			for _, id := range ids {
				if want <= 0 {
					break
				}
				// Not yet in picks (a second pass could see it again).
				dup := false
				for _, p := range picks {
					if p.id == id {
						dup = true
					}
				}
				if !dup {
					picks = append(picks, pick{id, name})
					want--
				}
			}
			if want <= 0 || lc == nil || topUps >= listTopUps {
				return nil
			}
			topUps++
			if err := topUpBank(d, lc, tiers[name]); err != nil {
				log.Printf("puzzle list: could not top up the bank: %v", err)
				return nil
			}
		}
		return nil
	}
	mix := listMix(own)
	for _, name := range tierOrder {
		if want := mix[name] - have[name]; want > 0 {
			if err := take(name, want); err != nil {
				return err
			}
		}
	}
	// A level with nothing left to give: fill from the pupil's own level,
	// then the others, so the list still reaches twenty.
	short := listSize - total - len(picks)
	for _, name := range append([]string{own}, tierOrder...) {
		if short <= 0 {
			break
		}
		before := len(picks)
		saved := topUps
		topUps = listTopUps // no more Lichess calls for borrowing
		if err := take(name, short); err != nil {
			return err
		}
		topUps = saved
		short -= len(picks) - before
	}

	// Mixed, not grouped by level: the new picks go into the free places in
	// an order that is stable for the pupil but not sorted by level.
	sort.SliceStable(picks, func(a, b int) bool { return mixKey(studentID, picks[a].id) < mixKey(studentID, picks[b].id) })
	pos := 1
	for _, p := range picks {
		for used[pos] {
			pos++
		}
		used[pos] = true
		if _, err := d.Exec(`INSERT OR IGNORE INTO puzzle_list (student_id, puzzle_id, position, tier, added_on)
		                     VALUES (?, ?, ?, ?, ?)`, studentID, p.id, pos, p.tier, day); err != nil {
			return err
		}
	}
	return nil
}

func mixKey(studentID, puzzleID string) uint32 {
	h := fnv.New32a()
	h.Write([]byte(studentID + "|" + puzzleID))
	return h.Sum32()
}

type listView struct {
	puzzleView
	Position int    `json:"position"`
	Tier     string `json:"tier"`
}

// handlePuzzleList returns the pupil's twenty, refreshed for today.
func handlePuzzleList(d *sql.DB, lc *lichess.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := requireIdentity(d, w, r)
		if id == nil {
			return
		}
		studentID, ok := studentOf(w, id)
		if !ok {
			return
		}
		if err := refreshList(d, lc, studentID); err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not prepare the puzzles", err)
			return
		}
		own, _ := pupilTier(d, studentID)
		rows, err := d.Query(`
			SELECT p.puzzle_id, p.fen, p.rating, p.themes, p.moves, l.position, l.tier, l.solved_on = ?
			FROM puzzle_list l JOIN puzzle p ON p.puzzle_id = l.puzzle_id
			WHERE l.student_id = ?
			ORDER BY l.position`, today(), studentID)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "query failed", err)
			return
		}
		defer rows.Close()
		out := []listView{}
		for rows.Next() {
			var v listView
			var moves string
			var solved sql.NullBool
			if err := rows.Scan(&v.PuzzleID, &v.FEN, &v.Rating, &v.Themes, &moves, &v.Position, &v.Tier, &solved); err != nil {
				httpx.Error(w, http.StatusInternalServerError, "query failed", err)
				return
			}
			// `moves` is read only to derive these two; it never leaves here.
			v.MoveCount = (len(strings.Fields(moves)) + 1) / 2
			v.Side, _ = puzzle.SideToMove(v.FEN)
			v.Solved = solved.Valid && solved.Bool
			out = append(out, v)
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"puzzles": out, "level": own})
	}
}

// handleListOpen stamps when a pupil opened a list puzzle, for the minutes its
// first solve adds to the day.
func handleListOpen(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := requireIdentity(d, w, r)
		if id == nil {
			return
		}
		studentID, ok := studentOf(w, id)
		if !ok {
			return
		}
		res, err := d.Exec(`UPDATE puzzle_list SET opened_at = datetime('now')
		                    WHERE student_id = ? AND puzzle_id = ? AND solved_on IS NULL AND opened_at IS NULL`,
			studentID, r.PathValue("id"))
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not record", err)
			return
		}
		n, _ := res.RowsAffected()
		httpx.JSON(w, http.StatusOK, map[string]any{"started": n > 0})
	}
}

// handleListAttempt grades one move of a list puzzle, as the daily grader
// does. The first solve is recorded; a replay is graded and nothing more.
func handleListAttempt(d *sql.DB) http.HandlerFunc {
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
		var solvedOn sql.NullString
		err := d.QueryRow(`SELECT p.fen, p.moves, l.solved_on FROM puzzle_list l
		                   JOIN puzzle p ON p.puzzle_id = l.puzzle_id
		                   WHERE l.student_id = ? AND l.puzzle_id = ?`, studentID, puzzleID).Scan(&fen, &solution, &solvedOn)
		if err != nil {
			httpx.Error(w, http.StatusNotFound, "that puzzle is not in your list", nil)
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
		firstSolve := verdict.Correct && verdict.Solved && !solvedOn.Valid
		if firstSolve {
			recordListSolve(d, studentID, puzzleID)
		}
		httpx.JSON(w, http.StatusOK, map[string]any{
			"correct":    verdict.Correct,
			"solved":     verdict.Solved,
			"reply":      verdict.Reply,
			"fen":        verdict.FEN,
			"firstSolve": firstSolve,
		})
	}
}

// recordListSolve ticks the puzzle and writes the solve where points, history
// and the practice record already look.
func recordListSolve(d *sql.DB, studentID, puzzleID string) {
	day := today()
	var opened sql.NullString
	d.QueryRow(`SELECT opened_at FROM puzzle_list WHERE student_id = ? AND puzzle_id = ?`, studentID, puzzleID).Scan(&opened)
	if _, err := d.Exec(`UPDATE puzzle_list SET solved_on = ? WHERE student_id = ? AND puzzle_id = ? AND solved_on IS NULL`,
		day, studentID, puzzleID); err != nil {
		log.Printf("puzzle list: ticking %s for %s: %v", puzzleID, studentID, err)
		return
	}
	if _, err := d.Exec(`INSERT OR IGNORE INTO puzzle_attempt
	                       (puzzle_attempt_id, student_id, puzzle_id, assigned_on, source, solved, solved_at, opened_at)
	                     VALUES (?, ?, ?, ?, 'free', 1, datetime('now'), ?)`,
		newID("pza"), studentID, puzzleID, day, opened); err != nil {
		log.Printf("puzzle list: recording %s for %s: %v", puzzleID, studentID, err)
		return
	}
	mins := 0
	if opened.Valid {
		if start, err := time.Parse(sqliteTimeLayout, opened.String); err == nil {
			mins = min(max(int(time.Since(start).Minutes()), 0), maxPuzzleMinutes)
		}
	}
	var solvedToday int
	if err := d.QueryRow(`SELECT COALESCE(SUM(solved),0) FROM puzzle_attempt WHERE student_id = ? AND assigned_on = ?`,
		studentID, day).Scan(&solvedToday); err == nil {
		if err := addPracticeMinutes(d, studentID, day, solvedToday, mins, solvedToday*pointsPerPuzzle); err != nil {
			log.Printf("puzzle list: recording practice for %s: %v", studentID, err)
		}
	}
}

func mountPuzzleList(mux *http.ServeMux, d *sql.DB, lc *lichess.Client) {
	mux.HandleFunc("GET /api/v1/puzzles/list", httpx.RateLimit(30, handlePuzzleList(d, lc)))
	mux.HandleFunc("POST /api/v1/puzzles/list/{id}/open", handleListOpen(d))
	mux.HandleFunc("POST /api/v1/puzzles/list/{id}/attempt", handleListAttempt(d))
}
