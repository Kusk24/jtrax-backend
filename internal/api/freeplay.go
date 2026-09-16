// Free Play: puzzles a pupil chooses by difficulty, outside the daily set.
//
// The daily challenge is three puzzles near the pupil's own rating, chosen for
// them and stable for the day. Free Play is the opposite question — "give me a
// hard one" — so the tier picks the rating band and there is no per-day limit.
//
// It draws from the same bank under the same never-repeat rule, which is what
// makes the two consistent: a puzzle a pupil has seen in either place is spent.
// That would empty a sixty-position bank quickly, so a tier with nothing left
// tops the bank up from Lichess rather than reporting itself empty.
package api

import (
	"database/sql"
	"errors"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/Kusk24/jtrax-backend/internal/httpx"
	"github.com/Kusk24/jtrax-backend/internal/lichess"
	"github.com/Kusk24/jtrax-backend/internal/puzzle"
)

// tier is one of the three difficulties the portal offers.
type tier struct {
	name    string
	minRate int
	maxRate int
	// Which Lichess band to ask for when the local bank runs dry here.
	// Without it every fetch returns ~1500 and the two lower tiers can never
	// be refilled — measured, not assumed.
	difficulty string
}

// The bands are the importer's 400–1600 range split three ways. They are the
// academy's own difficulty words, not Lichess's — a "beginner" puzzle here is
// beginner for a child at this school.
var tiers = map[string]tier{
	"beginner":     {"beginner", 0, 799, lichess.Easiest},
	"intermediate": {"intermediate", 800, 1199, lichess.Easier},
	"advanced":     {"advanced", 1200, 9999, lichess.Normal},
}

// topUpAttempts caps how many puzzles one request will pull from Lichess while
// looking for the band it wants. Every fetch is kept whatever its rating, so
// even a miss leaves the bank better off; the cap is there because a child
// pressing a button must not become an unbounded loop against someone else's
// API. Three is generous now that the difficulty band is asked for — the first
// fetch usually lands where it was wanted.
const topUpAttempts = 3

// handleFreePlayPuzzle hands out one unseen puzzle in the requested tier.
func handleFreePlayPuzzle(d *sql.DB, lc *lichess.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := requireIdentity(d, w, r)
		if id == nil {
			return
		}
		studentID, ok := studentOf(w, id)
		if !ok {
			return
		}

		t, ok := tiers[strings.ToLower(r.URL.Query().Get("tier"))]
		if !ok {
			httpx.Error(w, http.StatusBadRequest, "tier must be beginner, intermediate or advanced", nil)
			return
		}

		view, err := freePlayPuzzle(d, lc, studentID, t)
		if errors.Is(err, errTierEmpty) {
			// Not an error condition: the bank is spent for this pupil at this
			// difficulty and Lichess could not add to it. The portal says so
			// rather than showing an empty board.
			httpx.JSON(w, http.StatusOK, map[string]any{"puzzle": nil, "exhausted": true})
			return
		}
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not prepare a puzzle", err)
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"puzzle": view, "exhausted": false})
	}
}

// errTierEmpty means nothing is left to give at this difficulty.
var errTierEmpty = errors.New("free play: tier exhausted")

// freePlayPuzzle picks an unseen puzzle in the band, topping the bank up from
// Lichess when the band is spent, and records the assignment.
func freePlayPuzzle(d *sql.DB, lc *lichess.Client, studentID string, t tier) (*puzzleView, error) {
	for attempt := 0; ; attempt++ {
		view, err := assignFromBank(d, studentID, t)
		if err != nil {
			return nil, err
		}
		if view != nil {
			return view, nil
		}
		if attempt >= topUpAttempts || lc == nil {
			return nil, errTierEmpty
		}
		if err := topUpBank(d, lc, t); err != nil {
			// A pupil pressing a button should not see someone else's outage.
			// The bank may still hold something by the next press.
			log.Printf("free play: could not top up the bank: %v", err)
			return nil, errTierEmpty
		}
	}
}

// assignFromBank claims one unseen puzzle inside the band, or returns nil when
// the band holds none.
func assignFromBank(d *sql.DB, studentID string, t tier) (*puzzleView, error) {
	var (
		pid, fen, moves, themes string
		rating                  int
	)
	// Nearest the middle of the band first, so a tier does not hand out its
	// hardest puzzle to the first child who opens it.
	err := d.QueryRow(`
		SELECT puzzle_id, fen, moves, rating, themes FROM puzzle
		WHERE rating BETWEEN ? AND ?
		  AND puzzle_id NOT IN (SELECT puzzle_id FROM puzzle_attempt WHERE student_id = ?)
		ORDER BY ABS(rating - ?), puzzle_id LIMIT 1`,
		t.minRate, t.maxRate, studentID, (t.minRate+min(t.maxRate, 1600))/2,
	).Scan(&pid, &fen, &moves, &rating, &themes)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	// Recorded like a daily puzzle so history, practice minutes and the
	// never-repeat rule need no special case — but marked `free`, because the
	// daily endpoint reads every attempt dated today and would otherwise serve
	// this one as a fourth puzzle in a set of three.
	if _, err := d.Exec(`INSERT INTO puzzle_attempt (puzzle_attempt_id, student_id, puzzle_id, assigned_on, source)
	                     VALUES (?, ?, ?, ?, 'free')`, newID("pza"), studentID, pid, today()); err != nil {
		return nil, err
	}

	v := &puzzleView{PuzzleID: pid, FEN: fen, Rating: rating, Themes: themes}
	v.MoveCount = (len(strings.Fields(moves)) + 1) / 2
	v.Side, _ = puzzle.SideToMove(fen)
	return v, nil
}

// topUpBank fetches one puzzle from Lichess and stores it.
//
// Whatever arrives is kept, even when its rating misses the tier that asked:
// the caller loops, and a puzzle in the wrong band still fills a band somebody
// else will ask for.
func topUpBank(d *sql.DB, lc *lichess.Client, t tier) error {
	p, err := lc.NextPuzzle(t.difficulty)
	if err != nil {
		return err
	}
	// Validated before it is stored: an unsolvable row in the bank would
	// surface later as a grader that rejects every correct move.
	if err := puzzle.Validate(p.FEN, p.Moves); err != nil {
		return err
	}
	// Lichess ids are stable, so the same puzzle arriving twice is ignored
	// rather than duplicated under a second id.
	_, err = d.Exec(`INSERT OR IGNORE INTO puzzle (puzzle_id, fen, moves, rating, themes, source)
	                 VALUES (?, ?, ?, ?, ?, 'Lichess')`,
		"pzl_lichess_"+p.ID, p.FEN, p.Moves, p.Rating, p.Themes)
	return err
}

func mountFreePlay(mux *http.ServeMux, d *sql.DB) {
	lc := lichess.New()
	// Same override the rest of the Lichess surface uses, so a test can point
	// the whole integration at a stub without a second mechanism.
	if base := strings.TrimSpace(os.Getenv("LICHESS_API_BASE")); base != "" {
		lc.BaseURL = base
	}
	// Authenticated, but one press can spend up to topUpAttempts calls against
	// Lichess, so it is limited more tightly than the rest of the puzzle
	// surface. Ten a minute was too tight: solving auto-fetches the next
	// puzzle, so a child on a good run hit 429 in under a minute.
	mux.HandleFunc("GET /api/v1/puzzles/free", httpx.RateLimit(30, handleFreePlayPuzzle(d, lc)))
}
