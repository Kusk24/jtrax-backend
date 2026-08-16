package db

import (
	"strings"
	"testing"

	"github.com/Kusk24/jtrax-backend/internal/puzzle"
)

// Every puzzle that ships must be solvable from the position it ships with.
// A bad row here is a pupil stuck forever on a board that cannot be solved.
func TestEverySeededPuzzleIsSolvable(t *testing.T) {
	rows, err := ParsePuzzles(strings.NewReader(puzzlesCSV))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) < 20 {
		t.Fatalf("only %d seeded puzzles — the seed looks truncated", len(rows))
	}
	for _, p := range rows {
		if err := puzzle.Validate(p.FEN, p.Moves); err != nil {
			t.Errorf("puzzle %s does not play out: %v", p.ID, err)
		}
		if p.Rating < 300 || p.Rating > 1600 {
			t.Errorf("puzzle %s is rated %d, outside the beginner band", p.ID, p.Rating)
		}
	}
}
