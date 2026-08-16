package game_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/Kusk24/jtrax-backend/internal/game"
)

// play runs a list of UCI moves through Apply, failing on the first rejection.
func play(t *testing.T, moves ...string) []string {
	t.Helper()
	history := []string{}
	for _, m := range moves {
		if _, err := game.Apply(history, m); err != nil {
			t.Fatalf("move %q rejected: %v", m, err)
		}
		history = append(history, m)
	}
	return history
}

func TestApplyRecordsNotationAndPosition(t *testing.T) {
	a, err := game.Apply(nil, "e2e4")
	if err != nil {
		t.Fatalf("e2e4: %v", err)
	}
	if a.SAN != "e4" {
		t.Errorf("san = %q, want e4", a.SAN)
	}
	if a.Turn != "Black" {
		t.Errorf("turn = %q, want Black", a.Turn)
	}
	if !strings.HasPrefix(a.FEN, "rnbqkbnr/pppppppp/8/8/4P3/8/PPPP1PPP/RNBQKBNR b") {
		t.Errorf("fen = %q", a.FEN)
	}
	if a.Result != "" {
		t.Errorf("result = %q, want none", a.Result)
	}
}

// The whole reason the rules live on the server: a client that asks for a move
// the position does not allow has to be refused, not trusted.
func TestIllegalMovesAreRejected(t *testing.T) {
	cases := map[string]struct {
		prior []string
		move  string
	}{
		"pawn three squares":     {nil, "e2e5"},
		"knight to nowhere":      {nil, "b1b3"},
		"moving an empty square": {nil, "e4e5"},
		"moving the other side":  {nil, "e7e5"},
		"nonsense":               {nil, "zz9q9"},
		"empty":                  {nil, ""},
		// f3/g4 leaves white's king exposed on the e1-h4 diagonal; after Qh4#
		// the game is over and nothing more may be played.
		"after checkmate": {[]string{"f2f3", "e7e5", "g2g4", "d8h4"}, "e1f2"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := game.Apply(tc.prior, tc.move); err == nil {
				t.Fatalf("%q was accepted", tc.move)
			} else if !errors.Is(err, game.ErrIllegalMove) {
				t.Fatalf("want ErrIllegalMove, got %v", err)
			}
		})
	}
}

// Pinned pieces are what a naive "can this piece reach that square" check gets
// wrong, and the first thing a cheating client would reach for.
func TestAPinnedPieceCannotLeaveThePinLine(t *testing.T) {
	// 1. e4 e5 2. Nf3 d6 3. Bb5+ Bd7 4. Nc3 — the bishop on d7 now stands
	// between the b5 bishop and its own king on e8.
	h := play(t, "e2e4", "e7e5", "g1f3", "d7d6", "f1b5", "c8d7", "b1c3")

	if _, err := game.Apply(h, "d7g4"); !errors.Is(err, game.ErrIllegalMove) {
		t.Fatalf("the pinned bishop was allowed to abandon the king: %v", err)
	}
	// Moving along the pin line keeps the king covered, so it stays legal.
	if _, err := game.Apply(h, "d7c6"); err != nil {
		t.Fatalf("moving along the pin line should be legal: %v", err)
	}
}

func TestCheckmateEndsTheGame(t *testing.T) {
	// Fool's mate.
	a, err := game.Apply([]string{"f2f3", "e7e5", "g2g4"}, "d8h4")
	if err != nil {
		t.Fatalf("Qh4: %v", err)
	}
	if a.Result != "0-1" {
		t.Errorf("result = %q, want 0-1", a.Result)
	}
	if a.Reason != "Checkmate" {
		t.Errorf("reason = %q, want Checkmate", a.Reason)
	}
	if !a.Check {
		t.Error("checkmate should report check")
	}
	// SAN carries the # only because the move is matched against generated
	// moves rather than decoded — see findMove.
	if a.SAN != "Qh4#" {
		t.Errorf("san = %q, want Qh4#", a.SAN)
	}
}

// Threefold repetition is a property of the move history, not of the position,
// so this fails outright if a game is ever rebuilt from a stored FEN.
func TestThreefoldRepetitionIsDrawnAutomatically(t *testing.T) {
	// Knights out and back three times over.
	shuffle := []string{
		"g1f3", "g8f6", "f3g1", "f6g8",
		"g1f3", "g8f6", "f3g1", "f6g8",
		"g1f3", "g8f6", "f3g1",
	}
	play(t, shuffle...) // every step legal on its own
	a, err := game.Apply(shuffle, "f6g8")
	if err != nil {
		t.Fatalf("final move: %v", err)
	}
	if a.Result != "1/2-1/2" {
		t.Fatalf("result = %q (%s), want a draw", a.Result, a.Reason)
	}
	if a.Reason != "ThreefoldRepetition" {
		t.Errorf("reason = %q, want ThreefoldRepetition", a.Reason)
	}
}

func TestStalemateIsADraw(t *testing.T) {
	// Sam Loyd's ten-move stalemate: black is left with no legal move and is
	// not in check.
	h := play(t,
		"e2e3", "a7a5", "d1h5", "a8a6", "h5a5", "h7h5", "a5c7", "a6h6",
		"h2h4", "f7f6", "c7d7", "e8f7", "d7b7", "d8d3", "b7b8", "d3h7",
		"b8c8", "f7g6", "c8e6")
	st, err := game.Describe(h)
	if err != nil {
		t.Fatalf("describe: %v", err)
	}
	if st.Result != "1/2-1/2" || st.Reason != "Stalemate" {
		t.Fatalf("result = %q reason = %q, want a stalemate draw", st.Result, st.Reason)
	}
}

func TestDescribeListsEveryLegalMove(t *testing.T) {
	st, err := game.Describe(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Legal) != 20 {
		t.Errorf("opening has %d legal moves, want 20", len(st.Legal))
	}
	if st.Turn != "White" {
		t.Errorf("turn = %q, want White", st.Turn)
	}
	if st.FEN != game.StartFEN {
		t.Errorf("fen = %q, want the start position", st.FEN)
	}
}

func TestResignHandsTheGameToTheOtherColour(t *testing.T) {
	for _, tc := range []struct{ loser, want string }{
		{"White", "0-1"},
		{"Black", "1-0"},
	} {
		result, reason, err := game.Resign([]string{"e2e4"}, tc.loser)
		if err != nil {
			t.Fatalf("resign %s: %v", tc.loser, err)
		}
		if result != tc.want {
			t.Errorf("%s resigned: result = %q, want %q", tc.loser, result, tc.want)
		}
		if reason != "Resignation" {
			t.Errorf("reason = %q, want Resignation", reason)
		}
	}
}

func TestResignRejectsAFinishedGame(t *testing.T) {
	mated := []string{"f2f3", "e7e5", "g2g4", "d8h4"}
	if _, _, err := game.Resign(mated, "White"); !errors.Is(err, game.ErrIllegalMove) {
		t.Fatalf("want ErrIllegalMove, got %v", err)
	}
}

// Codes are read off a screen and typed by a child, so the characters that get
// confused for one another must never appear in one.
func TestCodesAvoidAmbiguousCharacters(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 500; i++ {
		c, err := game.Code()
		if err != nil {
			t.Fatal(err)
		}
		if len(c) != game.CodeLength {
			t.Fatalf("code %q has length %d, want %d", c, len(c), game.CodeLength)
		}
		if strings.ContainsAny(c, "IO01") {
			t.Fatalf("code %q contains an ambiguous character", c)
		}
		if c != strings.ToUpper(c) {
			t.Fatalf("code %q is not upper case", c)
		}
		seen[c] = true
	}
	// 500 draws from a 32^6 space colliding would mean the generator is not
	// random; a handful of duplicates is still astronomically unlikely.
	if len(seen) < 495 {
		t.Fatalf("only %d distinct codes in 500 draws", len(seen))
	}
}

func TestReplayRejectsACorruptHistory(t *testing.T) {
	if _, err := game.Replay([]string{"e2e4", "e2e4"}); err == nil {
		t.Fatal("replaying an impossible history should fail")
	}
}
