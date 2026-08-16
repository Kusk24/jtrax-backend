package puzzle_test

import (
	"errors"
	"testing"

	"github.com/Kusk24/jtrax-backend/internal/puzzle"
)

// Back-rank mate in one: the rook drops to a8 and the king has no escape.
const backRank = "6k1/5ppp/8/8/8/8/5PPP/R5K1 w - - 0 1"

// A real two-mover from the seeded Lichess set (001Wz, mateIn2), so the
// alternation between pupil and opponent is exercised against a line that is
// known to play: 1. Rd8+ Re8 2. Rxe8#
const twoMover = "6k1/5ppp/r1p5/p1n1rP2/8/2P2N1P/2P3P1/3R2K1 w - - 0 22"
const twoMoverLine = "d1d8 e5e8 d8e8"

func TestGradeAcceptsTheSolution(t *testing.T) {
	v, err := puzzle.Grade(backRank, "a1a8", nil, "a1a8")
	if err != nil {
		t.Fatal(err)
	}
	if !v.Correct || !v.Solved {
		t.Fatalf("correct=%v solved=%v, want both true", v.Correct, v.Solved)
	}
	if v.Reply != "" {
		t.Errorf("reply = %q, want none — the puzzle ends on the pupil's move", v.Reply)
	}
}

func TestGradeRejectsAnythingElse(t *testing.T) {
	for _, move := range []string{"a1a7", "g1h1", "f2f4", "zzzz", ""} {
		v, err := puzzle.Grade(backRank, "a1a8", nil, move)
		if err != nil {
			t.Fatalf("%q: %v", move, err)
		}
		if v.Correct {
			t.Errorf("%q was accepted", move)
		}
		if v.Solved {
			t.Errorf("%q reported the puzzle solved", move)
		}
	}
}

// A legal move that is not *the* move must still be refused — a puzzle has one
// answer, and accepting any legal move would make solving meaningless.
func TestALegalButWrongMoveIsRefused(t *testing.T) {
	v, err := puzzle.Grade(backRank, "a1a8", nil, "a1a4")
	if err != nil {
		t.Fatal(err)
	}
	if v.Correct {
		t.Fatal("a legal non-solution move was accepted")
	}
}

func TestTwoMoverPlaysTheOpponentsReply(t *testing.T) {
	v, err := puzzle.Grade(twoMover, twoMoverLine, nil, "d1d8")
	if err != nil {
		t.Fatalf("first move: %v", err)
	}
	if !v.Correct {
		t.Fatal("Rd8+ refused")
	}
	if v.Solved {
		t.Fatal("puzzle reported solved after one of two moves")
	}
	if v.Reply != "e5e8" {
		t.Fatalf("reply = %q, want e5e8 (the forced block)", v.Reply)
	}

	// `played` carries only the pupil's own moves — the opponent's reply is
	// reconstructed from the solution, so the client cannot misreport it.
	v2, err := puzzle.Grade(twoMover, twoMoverLine, []string{"d1d8"}, "d8e8")
	if err != nil {
		t.Fatalf("second move: %v", err)
	}
	if !v2.Correct || !v2.Solved {
		t.Fatalf("correct=%v solved=%v, want both true", v2.Correct, v2.Solved)
	}
}

// Skipping ahead must not work: the second move is only right after the first.
func TestMovesMustBePlayedInOrder(t *testing.T) {
	v, err := puzzle.Grade(twoMover, twoMoverLine, nil, "d8e8")
	if err != nil {
		t.Fatal(err)
	}
	if v.Correct {
		t.Fatal("the second move was accepted as the first")
	}
}

func TestValidateRejectsAnImpossibleSolution(t *testing.T) {
	for _, tc := range []struct{ name, fen, moves string }{
		{"move off the board", backRank, "a1a9"},
		{"piece that is not there", backRank, "h8h1"},
		{"empty solution", backRank, ""},
		{"nonsense position", "not-a-fen", "a1a8"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := puzzle.Validate(tc.fen, tc.moves); !errors.Is(err, puzzle.ErrBadPuzzle) {
				t.Fatalf("want ErrBadPuzzle, got %v", err)
			}
		})
	}
}

func TestValidateAcceptsAGoodPuzzle(t *testing.T) {
	if err := puzzle.Validate(backRank, "a1a8"); err != nil {
		t.Fatalf("a real mate-in-1 was rejected: %v", err)
	}
}

func TestSideToMoveOrientsTheBoard(t *testing.T) {
	side, err := puzzle.SideToMove(backRank)
	if err != nil {
		t.Fatal(err)
	}
	if side != "White" {
		t.Fatalf("side = %q, want White", side)
	}
	black := "r5k1/5ppp/8/8/8/8/5PPP/6K1 b - - 0 1"
	if side, _ := puzzle.SideToMove(black); side != "Black" {
		t.Fatalf("side = %q, want Black", side)
	}
}
