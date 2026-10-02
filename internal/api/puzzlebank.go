// The puzzle bank's levels, and topping the bank up from Lichess.
//
// Puzzles come from the academy's own bank: the embedded starter set and
// whatever cmd/importpuzzles loaded from the Lichess puzzle database. The
// practice list (puzzlelist.go) calls Lichess only when a level has run out
// for a pupil, a puzzle at a time, and keeps every puzzle it fetches for
// everyone. Free Play — one puzzle per press at a chosen level — used to live
// here; the list of twenty replaced it.
package api

import (
	"database/sql"
	"net/http"

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

func mountPuzzleBank(mux *http.ServeMux, d *sql.DB) {
	mountPuzzleList(mux, d, newPuzzleClient())
}
