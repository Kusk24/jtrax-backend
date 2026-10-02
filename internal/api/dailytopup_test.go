package api_test

/* The daily challenge never runs dry: when a pupil's band is spent, today's set
   is fetched from Lichess on the spot, and when it is running low the bank is
   refilled in the background so tomorrow is already waiting. */

import (
	"database/sql"
	"testing"
	"time"
)

// spendBank marks every puzzle in the bank as already set for Penny, as if she
// had solved her way through all of it, so nothing is left for her.
func spendBank(t *testing.T, d *sql.DB) {
	t.Helper()
	if _, err := d.Exec(`
		INSERT INTO puzzle_attempt (puzzle_attempt_id, student_id, puzzle_id, assigned_on)
		SELECT 'pza_spent_' || puzzle_id, 'stu_penny', puzzle_id, '2026-01-01' FROM puzzle`); err != nil {
		t.Fatal(err)
	}
}

func countPuzzles(d *sql.DB) int {
	var n int
	d.QueryRow(`SELECT COUNT(*) FROM puzzle`).Scan(&n)
	return n
}

func TestASpentBankIsToppedUpBeforeTodaysSetIsChosen(t *testing.T) {
	stub := newPuzzleStub(t, 720) // Lichess's "easiest" band, inside Penny's
	t.Setenv("LICHESS_API_BASE", stub.srv.URL)
	d := newDB(t)
	pupil := &client{t: t, srv: newServerOn(t, d)}
	pupil.login("penny@jca.ac.th")
	spendBank(t, d)

	set, obj := dailyReply(t, pupil)
	if len(set) != 3 {
		t.Fatalf("want a full set of 3 from Lichess, got %d (%v)", len(set), obj)
	}
	if obj["exhausted"] == true {
		t.Fatal("reported exhausted while Lichess was answering")
	}
	for _, p := range set {
		if r, _ := p["rating"].(float64); r != 720 {
			t.Errorf("puzzle %v is not one Lichess just supplied (rating %v)", p["puzzleId"], r)
		}
	}
	if stub.hits > 6+6 {
		t.Fatalf("one page load spent %d calls on Lichess", stub.hits)
	}
}

func TestALowBandIsRefilledInTheBackground(t *testing.T) {
	stub := newPuzzleStub(t, 720)
	t.Setenv("LICHESS_API_BASE", stub.srv.URL)
	d := newDB(t)
	pupil := &client{t: t, srv: newServerOn(t, d)}
	pupil.login("penny@jca.ac.th")
	spendBank(t, d)
	// Free one band puzzle per slot today, so the set fills from the bank
	// without a fetch — and leaves nothing behind for tomorrow.
	if _, err := d.Exec(`DELETE FROM puzzle_attempt WHERE puzzle_attempt_id IN (
		SELECT a.puzzle_attempt_id FROM puzzle_attempt a JOIN puzzle p USING (puzzle_id)
		WHERE a.student_id = 'stu_penny' AND ABS(p.rating - 800) <= 250 LIMIT 3)`); err != nil {
		t.Fatal(err)
	}
	before := countPuzzles(d)

	if set, _ := dailyReply(t, pupil); len(set) != 3 {
		t.Fatalf("want today's set from the bank, got %d", len(set))
	}
	// The refill happens after the reply, so wait for it rather than assume.
	deadline := time.Now().Add(5 * time.Second)
	for countPuzzles(d) < before+6 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if added := countPuzzles(d) - before; added < 6 {
		t.Fatalf("the background refill added %d puzzles, want 6", added)
	}
}

func TestAnOutageStillServesWhatTheBankHas(t *testing.T) {
	// The suite's default Lichess answers 503.
	d := newDB(t)
	pupil := &client{t: t, srv: newServerOn(t, d)}
	pupil.login("penny@jca.ac.th")

	set, obj := dailyReply(t, pupil)
	if len(set) != 3 || obj["exhausted"] == true {
		t.Fatalf("with Lichess down the seeded bank should still serve a full set, got %d (%v)", len(set), obj)
	}
}
