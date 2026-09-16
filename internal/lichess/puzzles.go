// Fetching puzzles from the public Lichess puzzle API, converted into the
// shape the academy's own bank stores.
//
// The embedded bank is sixty positions and the daily set never repeats one, so
// a pupil exhausts it in about three weeks. Free Play would empty it faster
// still. Rather than ship a feature that runs dry, the bank is topped up from
// Lichess on demand: a fetched puzzle is written to `puzzle` like any other, so
// assignment, grading and the never-repeat rule all work unchanged afterwards.
package lichess

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/notnil/chess"
)

// Puzzle is one position from the API, already converted to the bank's
// convention: `FEN` is the pupil to move and `Moves` alternates pupil,
// opponent, pupil.
type Puzzle struct {
	ID     string
	FEN    string
	Moves  string
	Rating int
	Themes string
}

// puzzleResponse is the subset of /api/puzzle/next this package reads.
//
// The API describes a puzzle as a whole game plus the ply it starts at, where
// the CSV database gives a FEN outright. Replaying is therefore not optional.
type puzzleResponse struct {
	Game struct {
		PGN string `json:"pgn"`
	} `json:"game"`
	Puzzle struct {
		ID         string   `json:"id"`
		Rating     int      `json:"rating"`
		Themes     []string `json:"themes"`
		Solution   []string `json:"solution"`
		InitialPly int      `json:"initialPly"`
	} `json:"puzzle"`
}

// Difficulty bands accepted by /api/puzzle/next. Measured unauthenticated,
// three samples each: easiest 662-778, easier 942-1147, normal 1516-1554,
// harder ~1750, hardest ~2150. The mapping matters — asking without one
// returns the normal band, which is why the low tiers could not be filled
// before this was passed.
const (
	Easiest = "easiest"
	Easier  = "easier"
	Normal  = "normal"
)

// NextPuzzle fetches one puzzle at the given difficulty.
//
// Difficulty is a band, not a guarantee: the caller still checks what arrived
// against the tier it wanted, and keeps it either way — a puzzle outside the
// band is still one the bank did not have.
func (c *Client) NextPuzzle(difficulty string) (*Puzzle, error) {
	url := c.base() + "/api/puzzle/next"
	if difficulty != "" {
		url += "?difficulty=" + difficulty
	}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("lichess: bad request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	res, err := c.http().Do(req)
	if err != nil {
		return nil, fmt.Errorf("lichess: unreachable: %w", err)
	}
	defer res.Body.Close()
	if err := status(res); err != nil {
		return nil, err
	}

	var body puzzleResponse
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("lichess: unreadable puzzle: %w", err)
	}
	return convert(&body)
}

// convert replays the game to the puzzle's starting ply and returns the
// position with the solution attached.
//
// The ply is inclusive: replaying through `initialPly` lands on the move the
// pupil is being asked to find, so `solution[0]` is *their* move. This differs
// from the CSV database, where the first entry is the opponent's move and has
// to be applied first — getting the two conventions the wrong way round yields
// a position whose solution is illegal, which is why it is asserted in a test
// rather than assumed here.
func convert(body *puzzleResponse) (*Puzzle, error) {
	if body.Puzzle.ID == "" || len(body.Puzzle.Solution) == 0 {
		return nil, fmt.Errorf("lichess: puzzle has no solution")
	}

	san := strings.Fields(body.Game.PGN)
	if body.Puzzle.InitialPly < 0 || body.Puzzle.InitialPly >= len(san) {
		return nil, fmt.Errorf("lichess: puzzle starts at ply %d of a %d-ply game",
			body.Puzzle.InitialPly, len(san))
	}

	game := chess.NewGame()
	notation := chess.AlgebraicNotation{}
	for i := 0; i <= body.Puzzle.InitialPly; i++ {
		move, err := notation.Decode(game.Position(), san[i])
		if err != nil {
			return nil, fmt.Errorf("lichess: unreplayable game at ply %d (%q): %w", i, san[i], err)
		}
		if err := game.Move(move); err != nil {
			return nil, fmt.Errorf("lichess: illegal move at ply %d (%q): %w", i, san[i], err)
		}
	}

	fen := game.Position().String()

	// A solution that does not run from the position we derived means the
	// convention moved under us. Refusing here keeps an unsolvable puzzle out
	// of the bank, where it would be indistinguishable from a broken grader.
	//
	// The whole solution is replayed, not merely its first move: checking one
	// move would still pass if the pupil's and opponent's moves were swapped,
	// which is exactly the mistake this guards against. `UCINotation.Decode`
	// parses squares without judging legality, so the move has to be played to
	// find out.
	start, err := chess.FEN(fen)
	if err != nil {
		return nil, fmt.Errorf("lichess: derived position is unreadable: %w", err)
	}
	check := chess.NewGame(start)
	uci := chess.UCINotation{}
	for i, move := range body.Puzzle.Solution {
		m, err := uci.Decode(check.Position(), move)
		if err != nil {
			return nil, fmt.Errorf("lichess: solution move %d (%q) unreadable: %w", i, move, err)
		}
		if err := check.Move(m); err != nil {
			return nil, fmt.Errorf("lichess: solution move %d (%q) is illegal: %w", i, move, err)
		}
	}

	return &Puzzle{
		ID:     body.Puzzle.ID,
		FEN:    fen,
		Moves:  strings.Join(body.Puzzle.Solution, " "),
		Rating: body.Puzzle.Rating,
		Themes: strings.Join(body.Puzzle.Themes, " "),
	}, nil
}
