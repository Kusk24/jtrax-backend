// importpuzzles loads the Lichess puzzle database into the puzzle table.
//
// The full dump is ~300 MB compressed and ~5 million puzzles, which would
// swamp a free-tier database and serve no one — a pupil will never reach the
// end of ten thousand. So this filters as it reads.
//
//	zstd -d --stdout lichess_db_puzzle.csv.zst | \
//	  go run ./cmd/importpuzzles -min 400 -max 1600 -limit 5000
//
// Download the source from https://database.lichess.org/ (CC0).
package main

import (
	"encoding/csv"
	"flag"
	"io"
	"log"
	"os"
	"strconv"
	"strings"

	"github.com/Kusk24/jtrax-backend/internal/db"
	"github.com/Kusk24/jtrax-backend/internal/puzzle"
	"github.com/notnil/chess"
)

func main() {
	var (
		minRating = flag.Int("min", 400, "lowest puzzle rating to import")
		maxRating = flag.Int("max", 1600, "highest puzzle rating to import")
		limit     = flag.Int("limit", 5000, "stop after this many puzzles")
		maxMoves  = flag.Int("max-moves", 5, "skip solutions longer than this many plies")
		dsn       = flag.String("db", os.Getenv("JTRAX_DB"), "database DSN (defaults to $JTRAX_DB)")
	)
	flag.Parse()
	if *dsn == "" {
		*dsn = "jtrax.db"
	}

	d, err := db.Open(*dsn)
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	defer d.Close()

	rows, skipped, err := read(os.Stdin, *minRating, *maxRating, *limit, *maxMoves)
	if err != nil {
		log.Fatalf("read: %v", err)
	}
	if err := db.InsertPuzzles(d, rows); err != nil {
		log.Fatalf("insert: %v", err)
	}
	log.Printf("imported %d puzzles into %s (skipped %d)", len(rows), db.Redact(*dsn), skipped)
}

// read parses the raw Lichess layout and normalises it.
//
// Lichess ships the position *before* the opponent's move, with that move first
// in the solution. Applying it here means everything downstream can treat `fen`
// as "your turn" without knowing where the puzzle came from.
func read(r io.Reader, minRating, maxRating, limit, maxMoves int) ([]db.Puzzle, int, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1 // the dump has trailing optional columns

	out := []db.Puzzle{}
	skipped := 0
	for len(out) < limit {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, skipped, err
		}
		if len(rec) < 8 || rec[0] == "PuzzleId" {
			continue
		}
		rating, err := strconv.Atoi(rec[3])
		if err != nil || rating < minRating || rating > maxRating {
			continue
		}
		moves := strings.Fields(rec[2])
		if len(moves) < 2 || len(moves) > maxMoves+1 {
			continue
		}

		fen, err := applySetupMove(rec[1], moves[0])
		if err != nil {
			skipped++
			continue
		}
		solution := strings.Join(moves[1:], " ")
		// Anything that does not play out is a bad row, not a puzzle. Better to
		// drop it here than to hand a pupil an unsolvable board.
		if err := puzzle.Validate(fen, solution); err != nil {
			skipped++
			continue
		}
		out = append(out, db.Puzzle{
			ID: rec[0], FEN: fen, Moves: solution, Rating: rating, Themes: rec[7],
		})
	}
	return out, skipped, nil
}

func applySetupMove(fen, uci string) (string, error) {
	setup, err := chess.FEN(fen)
	if err != nil {
		return "", err
	}
	game := chess.NewGame(setup)
	for _, mv := range game.ValidMoves() {
		if (chess.UCINotation{}).Encode(game.Position(), mv) == uci {
			if err := game.Move(mv); err != nil {
				return "", err
			}
			return game.Position().String(), nil
		}
	}
	return "", puzzle.ErrBadPuzzle
}
