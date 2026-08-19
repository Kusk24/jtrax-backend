// Authenticated play: pairing two students into a real game on lichess.org,
// relaying their moves to it, and following its state until it ends.
//
// Everything here needs a token belonging to the player whose account is acting.
// The academy has no account of its own in this flow and cannot acquire one —
// there is no "school" credential that can move a pupil's pieces, only the
// pupil's own grant.
//
// # Which side sends what
//
// A move is posted with the token of the player making it; Lichess rejects it
// otherwise, and rightly. Pairing needs two tokens: one to issue the challenge
// and one to accept it. So a rated game between two students is only possible
// once both have linked, which is a product constraint, not a limitation to
// engineer around.
package lichess

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// ChallengeParams describes the game to create.
type ChallengeParams struct {
	// Rated is the whole point of the exercise, but it is a caller's decision:
	// a coaching game between a teacher and a pupil should not move a child's
	// rating, and a lesson is not a competition.
	Rated bool
	// ClockLimit is the initial time in seconds, ClockIncrement the bonus per
	// move. Lichess only accepts 0, 15, 30, 45, 60, 90 and multiples of 60.
	ClockLimit     int
	ClockIncrement int
	// Color the *challenger* plays: "white", "black" or "random".
	Color string
}

// Challenge is a created challenge awaiting acceptance.
type Challenge struct {
	ID string `json:"id"`
	// Lichess reuses the challenge id as the game id once accepted, which is
	// why nothing here has to correlate the two.
	URL string `json:"url"`
}

type challengeEnvelope struct {
	ID     string     `json:"id"`
	URL    string     `json:"url"`
	Nested *Challenge `json:"challenge"`
}

// Challenge invites opponent to a game, as the holder of token.
func (c *Client) Challenge(ctx context.Context, token, opponent string, p ChallengeParams) (*Challenge, error) {
	if !ValidUsername(opponent) {
		return nil, ErrNotFound
	}
	form := url.Values{}
	form.Set("rated", boolText(p.Rated))
	form.Set("clock.limit", itoa(p.ClockLimit))
	form.Set("clock.increment", itoa(p.ClockIncrement))
	if p.Color != "" {
		form.Set("color", p.Color)
	}
	res, err := c.do(ctx, http.MethodPost, "/api/challenge/"+opponent, token, form)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if err := status(res); err != nil {
		return nil, err
	}
	// The reply has been documented both flat and wrapped in a `challenge`
	// object over the years. Accepting either costs one struct and saves an
	// outage the day it changes back.
	var env challengeEnvelope
	if err := json.NewDecoder(res.Body).Decode(&env); err != nil {
		return nil, fmt.Errorf("lichess: bad challenge response: %w", err)
	}
	if env.Nested != nil && env.Nested.ID != "" {
		return env.Nested, nil
	}
	if env.ID == "" {
		return nil, errors.New("lichess: challenge response had no id")
	}
	return &Challenge{ID: env.ID, URL: env.URL}, nil
}

// AcceptChallenge accepts a pending challenge as the holder of token.
func (c *Client) AcceptChallenge(ctx context.Context, token, challengeID string) error {
	res, err := c.do(ctx, http.MethodPost, "/api/challenge/"+url.PathEscape(challengeID)+"/accept", token, url.Values{})
	if err != nil {
		return err
	}
	defer res.Body.Close()
	return status(res)
}

// CancelChallenge withdraws a challenge that was never accepted.
func (c *Client) CancelChallenge(ctx context.Context, token, challengeID string) error {
	res, err := c.do(ctx, http.MethodPost, "/api/challenge/"+url.PathEscape(challengeID)+"/cancel", token, url.Values{})
	if err != nil {
		return err
	}
	defer res.Body.Close()
	return status(res)
}

// ErrMoveRejected means Lichess would not accept the move.
//
// Treated as a real answer rather than a transport failure: it means the two
// boards disagree, and the local one is wrong. Retrying cannot help.
var ErrMoveRejected = errors.New("lichess: move rejected")

// Move plays one move, in UCI, as the holder of token.
func (c *Client) Move(ctx context.Context, token, gameID, uci string) error {
	res, err := c.do(ctx, http.MethodPost,
		"/api/board/game/"+url.PathEscape(gameID)+"/move/"+url.PathEscape(uci), token, url.Values{})
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusBadRequest {
		body, _ := io.ReadAll(io.LimitReader(res.Body, 256))
		return fmt.Errorf("%w: %s", ErrMoveRejected, strings.TrimSpace(string(body)))
	}
	return status(res)
}

// Resign resigns the game as the holder of token.
func (c *Client) Resign(ctx context.Context, token, gameID string) error {
	res, err := c.do(ctx, http.MethodPost, "/api/board/game/"+url.PathEscape(gameID)+"/resign", token, url.Values{})
	if err != nil {
		return err
	}
	defer res.Body.Close()
	return status(res)
}

// Abort aborts a game before either side has committed to it.
func (c *Client) Abort(ctx context.Context, token, gameID string) error {
	res, err := c.do(ctx, http.MethodPost, "/api/board/game/"+url.PathEscape(gameID)+"/abort", token, url.Values{})
	if err != nil {
		return err
	}
	defer res.Body.Close()
	return status(res)
}

// GameState is one update from the game stream.
//
// Moves is the authoritative move list as a space-separated UCI string. It is
// the whole game every time rather than a delta, which is what makes the stream
// safe to reconnect to: a client that missed three updates is corrected by the
// next one instead of drifting.
type GameState struct {
	Type   string `json:"type"`
	Moves  string `json:"moves"`
	WTime  int    `json:"wtime"`
	BTime  int    `json:"btime"`
	Status string `json:"status"`
	Winner string `json:"winner"`
}

// gameFull wraps the first line of the stream, whose state is nested.
type gameFull struct {
	Type  string    `json:"type"`
	ID    string    `json:"id"`
	State GameState `json:"state"`
}

// Finished reports whether a status means the game is over.
func Finished(status string) bool {
	switch status {
	case "", "created", "started":
		return false
	}
	return true
}

// StreamGame follows a game until it ends, calling onState for every update.
//
// The stream stays open for the length of a game, so the usual 15-second client
// timeout would kill it mid-play; this uses a client without one and relies on
// ctx for cancellation instead.
//
// Lichess sends a blank line as a keep-alive. Those are skipped rather than
// parsed, which is also what stops a stalled connection looking like activity.
func (c *Client) StreamGame(ctx context.Context, token, gameID string, onState func(GameState)) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.base()+"/api/board/game/stream/"+url.PathEscape(gameID), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	streamer := &http.Client{Timeout: 0}
	if c.HTTP != nil {
		streamer.Transport = c.HTTP.Transport
	}
	res, err := streamer.Do(req)
	if err != nil {
		return fmt.Errorf("lichess: stream unreachable: %w", err)
	}
	defer res.Body.Close()
	if err := status(res); err != nil {
		return err
	}

	sc := bufio.NewScanner(res.Body)
	// A game's move list grows without bound relative to the default 64KB
	// scanner buffer only in theory, but a long correspondence game is exactly
	// the case nobody tests, so the ceiling is raised deliberately.
	sc.Buffer(make([]byte, 0, 8*1024), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var probe struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal([]byte(line), &probe); err != nil {
			continue
		}
		switch probe.Type {
		case "gameFull":
			var full gameFull
			if err := json.Unmarshal([]byte(line), &full); err == nil {
				full.State.Type = "gameState"
				onState(full.State)
			}
		case "gameState":
			var st GameState
			if err := json.Unmarshal([]byte(line), &st); err == nil {
				onState(st)
			}
		}
		// chatLine and opponentGone are deliberately ignored: the academy does
		// not relay chat, and Kid Mode accounts cannot send any.
	}
	if err := sc.Err(); err != nil && ctx.Err() == nil {
		return fmt.Errorf("lichess: stream ended badly: %w", err)
	}
	return nil
}

/* ---- plumbing ---- */

func boolText(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func itoa(n int) string { return fmt.Sprintf("%d", n) }
