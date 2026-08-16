package db

import (
	"database/sql"
	_ "embed"
	"encoding/csv"
	"fmt"
	"strconv"
	"strings"
)

// puzzlesCSV is a small starter set so a fresh clone — and a fresh deployment —
// has something to solve. Filtered from the Lichess puzzle database to ratings
// a beginner can handle and themes a chess school teaches.
//
// That database is CC0 (public domain), so unlike the engine there is no
// licence condition on redistributing it. Load the full set with
// `cmd/importpuzzles`; see docs/puzzles.md.
//
//go:embed puzzles.csv
var puzzlesCSV string

// SeedPuzzles loads the starter set if the table is empty.
//
// Unlike Seed this runs everywhere, local and remote: puzzles are content, not
// demo accounts, so there is nothing here that publishing could leak.
func SeedPuzzles(d *sql.DB) error {
	var n int
	if err := d.QueryRow(`SELECT COUNT(*) FROM puzzle`).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	rows, err := ParsePuzzles(strings.NewReader(puzzlesCSV))
	if err != nil {
		return err
	}
	return InsertPuzzles(d, rows)
}

// Puzzle is one row on its way into the table.
type Puzzle struct {
	ID     string
	FEN    string
	Moves  string
	Rating int
	Themes string
}

// ParsePuzzles reads the CSV shape used by both the embedded seed and the
// importer: puzzle_id, fen, moves, rating, themes.
//
// Note this is *not* the raw Lichess layout — the importer normalises that
// first, applying the opponent's setup move so `fen` is the position the pupil
// actually sees.
func ParsePuzzles(r interface{ Read(p []byte) (int, error) }) ([]Puzzle, error) {
	cr := csv.NewReader(r)
	records, err := cr.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("read puzzles: %w", err)
	}
	out := []Puzzle{}
	for i, rec := range records {
		if i == 0 && rec[0] == "puzzle_id" {
			continue // header
		}
		if len(rec) < 5 {
			return nil, fmt.Errorf("puzzle row %d has %d columns, want 5", i+1, len(rec))
		}
		rating, err := strconv.Atoi(strings.TrimSpace(rec[3]))
		if err != nil {
			return nil, fmt.Errorf("puzzle row %d: bad rating %q", i+1, rec[3])
		}
		out = append(out, Puzzle{ID: rec[0], FEN: rec[1], Moves: rec[2], Rating: rating, Themes: rec[4]})
	}
	return out, nil
}

// InsertPuzzles writes rows, skipping ids that are already present so a re-run
// tops up rather than failing.
func InsertPuzzles(d *sql.DB, rows []Puzzle) error {
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, p := range rows {
		if _, err := tx.Exec(
			`INSERT OR IGNORE INTO puzzle (puzzle_id, fen, moves, rating, themes, source)
			 VALUES (?, ?, ?, ?, ?, 'Lichess')`,
			p.ID, p.FEN, p.Moves, p.Rating, p.Themes); err != nil {
			return fmt.Errorf("insert puzzle %s: %w", p.ID, err)
		}
	}
	return tx.Commit()
}
