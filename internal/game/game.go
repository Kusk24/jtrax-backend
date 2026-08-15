// Package game holds the chess rules and room-code logic behind the
// play-a-friend feature. It knows nothing about HTTP or storage: the API layer
// hands it a move history, it hands back a validated move or an error.
//
// Rules live on the server because the client cannot be trusted to grade its
// own game. A browser that posts "Qxh7#" from a position where the queen was
// captured twenty moves ago is not a bug to tolerate — it is how a student
// wins every game.
package game

import (
	"crypto/rand"
	"errors"
	"fmt"
	"strings"

	"github.com/notnil/chess"
)

// StartFEN is the standard opening position, stored on a room at creation so
// every read path has a position to show before the first move is played.
const StartFEN = "rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq - 0 1"

// codeAlphabet omits I, O, 0 and 1. A code is read off a screen and typed by a
// child, and those four characters are the ones that get mistyped.
const codeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// CodeLength gives 32^6 ≈ 1.07 billion codes. Combined with the rate limit on
// joining, guessing one is not a practical attack.
const CodeLength = 6

// ErrIllegalMove is returned for any move the rules reject, whatever the
// reason. The caller maps it to 400 without elaborating: telling a client
// exactly why its move was refused helps nobody but a client that is probing.
var ErrIllegalMove = errors.New("game: illegal move")

// Code returns a fresh room code. Callers retry on a unique-constraint
// collision rather than checking first, which would race.
func Code() (string, error) {
	raw := make([]byte, CodeLength)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	// len(codeAlphabet) is 32 and divides 256, so the modulo is unbiased.
	out := make([]byte, CodeLength)
	for i, b := range raw {
		out[i] = codeAlphabet[int(b)%len(codeAlphabet)]
	}
	return string(out), nil
}

// Replay rebuilds a game from its move list.
//
// Rebuilding from moves rather than from the stored FEN is deliberate: the
// threefold-repetition and fifty-move draws are properties of the history, not
// of the position, and a game restored from a bare FEN can never claim them.
func Replay(uciMoves []string) (*chess.Game, error) {
	g := chess.NewGame()
	for i, m := range uciMoves {
		mv, err := chess.UCINotation{}.Decode(g.Position(), m)
		if err != nil {
			return nil, fmt.Errorf("game: move %d (%q) does not decode: %w", i+1, m, err)
		}
		if err := g.Move(mv); err != nil {
			return nil, fmt.Errorf("game: move %d (%q) is not legal: %w", i+1, m, err)
		}
	}
	return g, nil
}

// Applied describes one accepted half-move and the state it produced.
type Applied struct {
	SAN    string
	UCI    string
	FEN    string
	Turn   string // colour to move after this move: "White" or "Black"
	Result string // "" while the game continues, else 1-0 / 0-1 / 1/2-1/2
	Reason string // Checkmate, Stalemate, … — set with Result
	Check  bool
}

// Apply validates one UCI move against the position reached by prior.
//
// It returns ErrIllegalMove for anything the rules refuse, so a caller can
// distinguish a bad request from a corrupt history without inspecting strings.
func Apply(prior []string, uci string) (*Applied, error) {
	g, err := Replay(prior)
	if err != nil {
		return nil, err
	}
	if g.Outcome() != chess.NoOutcome {
		return nil, fmt.Errorf("%w: the game is already over", ErrIllegalMove)
	}
	uci = strings.ToLower(strings.TrimSpace(uci))
	mv := findMove(g, uci)
	if mv == nil {
		return nil, fmt.Errorf("%w: %s", ErrIllegalMove, uci)
	}
	// Both notations have to be read off the position *before* the move: SAN
	// is disambiguated against the pieces that could also have moved there,
	// and neither encoder can see a position the move has already left.
	san := chess.AlgebraicNotation{}.Encode(g.Position(), mv)
	norm := chess.UCINotation{}.Encode(g.Position(), mv)
	if err := g.Move(mv); err != nil {
		return nil, fmt.Errorf("%w: %s", ErrIllegalMove, uci)
	}
	claimAutomaticDraw(g)

	a := &Applied{
		SAN:   san,
		UCI:   norm,
		FEN:   g.Position().String(),
		Turn:  colorName(g.Position().Turn()),
		Check: mv.HasTag(chess.Check),
	}
	if o := g.Outcome(); o != chess.NoOutcome {
		a.Result = o.String()
		a.Reason = g.Method().String()
	}
	return a, nil
}

// claimAutomaticDraw takes threefold repetition and the fifty-move rule on the
// players' behalf.
//
// Both are *claimable* in tournament chess — the engine offers them and a
// player decides. There is no UI here to make that decision and no clock to
// run out, so a game that reaches either would otherwise continue forever.
// Drawing them automatically is what a casual online board does.
func claimAutomaticDraw(g *chess.Game) {
	if g.Outcome() != chess.NoOutcome {
		return
	}
	for _, m := range g.EligibleDraws() {
		if m == chess.ThreefoldRepetition || m == chess.FiftyMoveRule {
			g.Draw(m)
			return
		}
	}
}

// Resign ends a game in favour of the other colour. Colour is resolved from
// the seat the caller holds, never from the request body.
func Resign(prior []string, loser string) (result, reason string, err error) {
	g, err := Replay(prior)
	if err != nil {
		return "", "", err
	}
	if g.Outcome() != chess.NoOutcome {
		return "", "", fmt.Errorf("%w: the game is already over", ErrIllegalMove)
	}
	c := chess.White
	if loser == "Black" {
		c = chess.Black
	}
	g.Resign(c)
	return g.Outcome().String(), g.Method().String(), nil
}

// Status summarises a stored game for a client that is joining or reconnecting.
type Status struct {
	FEN    string   `json:"fen"`
	Turn   string   `json:"turn"`
	Result string   `json:"result,omitempty"`
	Reason string   `json:"reason,omitempty"`
	Legal  []string `json:"legalMoves"`
}

// Describe replays a history and reports the position plus every legal move.
//
// Shipping the legal-move list means the board can highlight destinations
// without the client re-deriving the rules, and keeps the two sides agreeing on
// what is playable — the client's own check is a convenience, not the referee.
func Describe(uciMoves []string) (*Status, error) {
	g, err := Replay(uciMoves)
	if err != nil {
		return nil, err
	}
	claimAutomaticDraw(g)
	s := &Status{
		FEN:   g.Position().String(),
		Turn:  colorName(g.Position().Turn()),
		Legal: []string{},
	}
	if o := g.Outcome(); o != chess.NoOutcome {
		s.Result = o.String()
		s.Reason = g.Method().String()
		return s, nil
	}
	for _, mv := range g.ValidMoves() {
		s.Legal = append(s.Legal, chess.UCINotation{}.Encode(g.Position(), mv))
	}
	return s, nil
}

// findMove resolves a UCI string against the legal moves in a position.
//
// Matching a generated move rather than decoding the string is what makes the
// check and checkmate tags available — a decoded move carries no tags, so its
// SAN comes out as a bare "Qh4" for a move that ends the game. It also means
// legality is decided by the generator alone, with no second code path.
func findMove(g *chess.Game, uci string) *chess.Move {
	for _, mv := range g.ValidMoves() {
		if (chess.UCINotation{}).Encode(g.Position(), mv) == uci {
			return mv
		}
	}
	return nil
}

func colorName(c chess.Color) string {
	if c == chess.Black {
		return "Black"
	}
	return "White"
}
