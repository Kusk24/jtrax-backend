package lichess

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/notnil/chess"
)

// A real /api/puzzle/next body, trimmed to the fields the converter reads.
// The game is replayed to ply 88, which leaves Black to move and b4d5 legal.
const sampleBody = `{
  "game": {
    "pgn": "e4 e5 Nf3 Nc6 Bc4 h6 d4 exd4 Nxd4 Nxd4 Qxd4 d6 Qd5 Be6 Qb5+ c6 Qb3 d5 exd5 Bxd5 O-O Bxc4 Qxc4 Be7 Re1 Qc7 Bf4 Qd7 Nc3 O-O-O Qe4 Bf6 Rad1 Qe6 Rxd8+ Bxd8 Qxe6+ fxe6 Rxe6 Nf6 f3 Bc7 Ne2 Bxf4 Nxf4 Rf8 Re7 g5 Ng6 Rd8 Re6 Nd5 c4 Nb4 Ne7+ Kd7 Re3 Re8 Nf5 Rxe3 Nxe3 Nxa2 Nf5 h5 Kf2 Ke6 Ng7+ Kf6 Nxh5+ Kf5 g4+ Kg6 Ng3 Nb4 Ke3 b5 b3 a5 cxb5 cxb5 f4 gxf4+ Kxf4 a4 bxa4 bxa4 Ne2 a3 Nc3"
  },
  "puzzle": {
    "id": "TESTPZ",
    "rating": 1410,
    "solution": [
      "b4d5",
      "c3d5",
      "a3a2",
      "d5c3",
      "a2a1q"
    ],
    "themes": [
      "endgame",
      "mateIn3"
    ],
    "initialPly": 88
  }
}`

// The convention is the whole risk in this converter. Lichess's CSV database
// puts the opponent's move first and the API does not; applying the CSV rule to
// an API body yields a position whose solution is illegal, and the puzzle would
// reach a child as a board that rejects every correct answer.
func TestTheSolutionIsLegalFromTheConvertedPosition(t *testing.T) {
	var body puzzleResponse
	if err := json.Unmarshal([]byte(sampleBody), &body); err != nil {
		t.Fatalf("sample body: %v", err)
	}
	got, err := convert(&body)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}

	fen, err := chess.FEN(got.FEN)
	if err != nil {
		t.Fatalf("converted FEN is not a position: %v", err)
	}
	game := chess.NewGame(fen)

	// Every move of the solution must be legal in turn — not just the first,
	// which would pass even if the pupil and opponent moves were swapped.
	uci := chess.UCINotation{}
	for i, move := range strings.Fields(got.Moves) {
		m, err := uci.Decode(game.Position(), move)
		if err != nil {
			t.Fatalf("solution move %d (%s) is illegal: %v", i, move, err)
		}
		if err := game.Move(m); err != nil {
			t.Fatalf("solution move %d (%s) rejected: %v", i, move, err)
		}
	}
}

// The pupil is the side to move in the stored position: that is what lets the
// portal orient the board without being told.
func TestTheStoredPositionIsThePupilsTurn(t *testing.T) {
	var body puzzleResponse
	json.Unmarshal([]byte(sampleBody), &body)
	got, _ := convert(&body)

	fields := strings.Fields(got.FEN)
	if len(fields) < 2 || fields[1] != "b" {
		t.Fatalf("expected Black to move in %q", got.FEN)
	}
	if got.Rating != 1410 || got.ID != "TESTPZ" {
		t.Fatalf("metadata lost: %+v", got)
	}
	if got.Themes != "endgame mateIn3" {
		t.Fatalf("themes = %q", got.Themes)
	}
}

// A body whose solution does not fit the position it derived is refused rather
// than stored. An unsolvable row in the bank is indistinguishable from a broken
// grader once a child is looking at it.
func TestAPuzzleWhoseSolutionDoesNotFitIsRefused(t *testing.T) {
	var body puzzleResponse
	json.Unmarshal([]byte(sampleBody), &body)
	body.Puzzle.Solution = []string{"a1a8"} // not legal in that position

	if _, err := convert(&body); err == nil {
		t.Fatal("expected a puzzle with an impossible solution to be refused")
	}
}

func TestNextPuzzleReadsTheAPI(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/puzzle/next" {
			t.Errorf("asked for %s", r.URL.Path)
		}
		// The band has to travel, or every fetch comes back around 1500 and
		// the two easier tiers can never be refilled.
		if got := r.URL.Query().Get("difficulty"); got != Easiest {
			t.Errorf("difficulty = %q, wanted %q", got, Easiest)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(sampleBody))
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL}
	got, err := c.NextPuzzle(Easiest)
	if err != nil {
		t.Fatalf("NextPuzzle: %v", err)
	}
	if got.ID != "TESTPZ" {
		t.Fatalf("got %+v", got)
	}
}
