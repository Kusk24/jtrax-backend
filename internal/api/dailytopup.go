// Keeping the daily challenge from ever running dry.
//
// The daily set never repeats a puzzle, and the bank shipped with sixty, so a
// pupil set three a day used to reach "You've solved every puzzle we have!" in
// about twenty days. The practice list (puzzlelist.go) refills its own levels
// from Lichess; the daily set did not. Now it does, in two ways:
//
//   - When today's set cannot be filled from the pupil's rating band, the bank
//     is topped up on the spot — a few fetches at most — before the set is
//     chosen. That is the guarantee.
//   - When a set has just been chosen and fewer than reserveDays' worth of
//     unseen puzzles are left in the band, a few more are fetched in the
//     background, so tomorrow's set is already waiting and the pupil never
//     waits on Lichess at all.
//
// Fetched puzzles go into the shared bank, so every pupil near that rating
// benefits from the first one who needed them.
package api

import (
	"database/sql"
	"log"
	"os"
	"strings"
	"sync"

	"github.com/Kusk24/jtrax-backend/internal/lichess"
)

// reserveDays is how many days of sets a pupil should have waiting in their
// band before a background top-up is started.
const reserveDays = 3

// dailyTopUpAttempts caps the fetches one request may make, on the spot or in
// the background. Lichess rate-limits, and a page load must never become an
// unbounded loop against someone else's API.
const dailyTopUpAttempts = 6

// newPuzzleClient is the Lichess client the puzzle endpoints share, with the
// same LICHESS_API_BASE override the rest of the Lichess surface uses so a test
// can point it at a stub.
func newPuzzleClient() *lichess.Client {
	lc := lichess.New()
	if base := strings.TrimSpace(os.Getenv("LICHESS_API_BASE")); base != "" {
		lc.BaseURL = base
	}
	return lc
}

// dailyDifficulty is the Lichess band nearest a pupil's rating. The bands were
// measured unauthenticated (see internal/lichess): easiest ~660-780, easier
// ~940-1150, normal ~1530, harder ~1750, hardest ~2150.
func dailyDifficulty(rating int) string {
	switch {
	case rating < 850:
		return lichess.Easiest
	case rating < 1300:
		return lichess.Easier
	case rating < 1650:
		return lichess.Normal
	case rating < 1950:
		return lichess.Harder
	default:
		return lichess.Hardest
	}
}

// unseenInBand counts the puzzles in the pupil's daily band (dailyTarget) that
// they have never been set and that are not waiting on their practice list —
// the same two exclusions the daily choice makes.
func unseenInBand(d *sql.DB, studentID string, band tier) (int, error) {
	var n int
	err := d.QueryRow(`
		SELECT COUNT(*) FROM puzzle
		WHERE rating BETWEEN ? AND ?
		  AND puzzle_id NOT IN (SELECT puzzle_id FROM puzzle_attempt WHERE student_id = ?)
		  AND puzzle_id NOT IN (SELECT puzzle_id FROM puzzle_list WHERE student_id = ?)`,
		band.minRate, band.maxRate, studentID, studentID).Scan(&n)
	return n, err
}

// bandDifficulty is the Lichess band to ask for: the level's own when the pupil
// has one (puzzlebank.go's tiers), else the nearest to their rating.
func bandDifficulty(target int, band tier) string {
	if band.difficulty != "" {
		return band.difficulty
	}
	return dailyDifficulty(target)
}

// fetchForBand pulls up to `want` puzzles at the pupil's difficulty into the
// bank, stopping at the first failure. Whatever arrives is kept even if it
// lands outside the band — it is still a puzzle the bank did not have.
func fetchForBand(d *sql.DB, lc *lichess.Client, target int, band tier, want int) int {
	if lc == nil {
		return 0
	}
	t := tier{difficulty: bandDifficulty(target, band)}
	added := 0
	for i := 0; i < want && i < dailyTopUpAttempts; i++ {
		if err := topUpBank(d, lc, t); err != nil {
			// The pupil still gets whatever the bank holds; an outage at
			// Lichess is logged, not shown to a child.
			log.Printf("daily puzzles: could not top up the bank: %v", err)
			break
		}
		added++
	}
	return added
}

// topUpsRunning keeps one background top-up per difficulty band at a time, so
// a class opening the app together does not send thirty requests to Lichess.
var topUpsRunning sync.Map

// topUpInBackground refills the pupil's band without making them wait.
func topUpInBackground(d *sql.DB, lc *lichess.Client, target int, band tier, want int) {
	key := bandDifficulty(target, band)
	if _, busy := topUpsRunning.LoadOrStore(key, true); busy {
		return
	}
	go func() {
		defer topUpsRunning.Delete(key)
		fetchForBand(d, lc, target, band, want)
	}()
}
