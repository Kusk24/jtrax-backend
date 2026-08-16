// Package puzzle grades tactics attempts and validates puzzles on the way in.
//
// Grading is here rather than in the browser because a solution the client
// holds is a solution the client can read. These attempts feed streaks and
// practice records a parent sees, so they have to mean something.
package puzzle

import (
	"errors"
	"strings"

	"github.com/notnil/chess"
)

// ErrBadPuzzle means the stored solution does not play out from the stored
// position — a corrupt import, or a teacher's typo.
var ErrBadPuzzle = errors.New("puzzle: solution does not play from this position")

// Verdict is the outcome of one attempt.
type Verdict struct {
	// Correct reports whether the move continues the solution.
	Correct bool
	// Solved is true when that move was the last one required.
	Solved bool
	// Reply is the opponent's answer, played automatically. Empty when the
	// puzzle ends on the pupil's move — which is most of them, since a mate
	// leaves nothing to reply with.
	Reply string
	// FEN after the move and any reply, so the board can be redrawn from one
	// authoritative source rather than the client's own guess.
	FEN string
}

// solutionMoves splits the stored solution. Entries alternate: pupil, opponent,
// pupil, … so the even indices are the ones the pupil has to find.
func solutionMoves(solution string) []string {
	return strings.Fields(solution)
}

// Grade checks one move against the solution.
//
// `played` is the pupil's own moves so far, not the whole line — the opponent's
// replies come from the solution, so the server can rebuild the position
// without trusting the client to report it.
func Grade(fen, solution string, played []string, move string) (*Verdict, error) {
	moves := solutionMoves(solution)
	if len(moves) == 0 {
		return nil, ErrBadPuzzle
	}
	// The pupil moves on even indices; each of their moves consumes two entries
	// unless the solution ends there.
	idx := len(played) * 2
	if idx >= len(moves) {
		return nil, ErrBadPuzzle
	}

	game, err := replay(fen, moves[:idx])
	if err != nil {
		return nil, err
	}

	want := moves[idx]
	got := strings.ToLower(strings.TrimSpace(move))
	if got != want {
		// Deliberately no hint about what the right move was.
		return &Verdict{Correct: false, FEN: game.Position().String()}, nil
	}
	if err := play(game, want); err != nil {
		return nil, err
	}

	v := &Verdict{Correct: true}
	if idx+1 < len(moves) {
		v.Reply = moves[idx+1]
		if err := play(game, v.Reply); err != nil {
			return nil, err
		}
	}
	v.Solved = idx+2 >= len(moves)
	v.FEN = game.Position().String()
	return v, nil
}

// Validate reports whether a solution plays out legally from a position. Used
// on import and whenever a teacher authors a puzzle, so a broken one is a 400
// at the boundary rather than a pupil stuck on an unsolvable board.
func Validate(fen, solution string) error {
	moves := solutionMoves(solution)
	if len(moves) == 0 {
		return ErrBadPuzzle
	}
	_, err := replay(fen, moves)
	return err
}

// SideToMove reports which colour the pupil plays, for orienting the board.
func SideToMove(fen string) (string, error) {
	game, err := replay(fen, nil)
	if err != nil {
		return "", err
	}
	if game.Position().Turn() == chess.Black {
		return "Black", nil
	}
	return "White", nil
}

func replay(fen string, moves []string) (*chess.Game, error) {
	setup, err := chess.FEN(fen)
	if err != nil {
		return nil, ErrBadPuzzle
	}
	game := chess.NewGame(setup)
	for _, m := range moves {
		if err := play(game, m); err != nil {
			return nil, err
		}
	}
	return game, nil
}

// play applies a UCI move, matching it against generated moves so legality is
// decided by the generator alone.
func play(game *chess.Game, uci string) error {
	for _, mv := range game.ValidMoves() {
		if (chess.UCINotation{}).Encode(game.Position(), mv) == uci {
			return game.Move(mv)
		}
	}
	return ErrBadPuzzle
}
