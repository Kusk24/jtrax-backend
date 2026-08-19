package lichess_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Kusk24/jtrax-backend/internal/lichess"
)

// Every authenticated call must carry the player's own token. Posting a move
// with the wrong side's credential is the defining bug of this feature, so the
// header is asserted rather than assumed.
func TestMoveUsesTheMoversToken(t *testing.T) {
	var gotAuth, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotPath = r.Header.Get("Authorization"), r.URL.Path
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c := &lichess.Client{BaseURL: srv.URL}
	if err := c.Move(context.Background(), "tok_white", "abcd1234", "e2e4"); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer tok_white" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if gotPath != "/api/board/game/abcd1234/move/e2e4" {
		t.Errorf("path = %q", gotPath)
	}
}

// A 400 from Lichess means the boards disagree. That is a different thing from
// the network being down, and the caller has to be able to tell: one is worth
// retrying and the other never is.
func TestMoveRejectionIsDistinguishable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"Not your turn, or game already over"}`))
	}))
	defer srv.Close()

	c := &lichess.Client{BaseURL: srv.URL}
	err := c.Move(context.Background(), "tok", "g1", "e2e4")
	if !errors.Is(err, lichess.ErrMoveRejected) {
		t.Fatalf("err = %v, want ErrMoveRejected", err)
	}
}

func TestChallengeSendsRatedAndClock(t *testing.T) {
	var form map[string][]string
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		form, path = r.PostForm, r.URL.Path
		_, _ = w.Write([]byte(`{"id":"chal123","url":"https://lichess.org/chal123"}`))
	}))
	defer srv.Close()

	c := &lichess.Client{BaseURL: srv.URL}
	ch, err := c.Challenge(context.Background(), "tok", "Penny_Plays", lichess.ChallengeParams{
		Rated: true, ClockLimit: 600, ClockIncrement: 5, Color: "white",
	})
	if err != nil {
		t.Fatal(err)
	}
	if ch.ID != "chal123" {
		t.Errorf("id = %q", ch.ID)
	}
	if path != "/api/challenge/Penny_Plays" {
		t.Errorf("path = %q", path)
	}
	for k, want := range map[string]string{
		"rated": "true", "clock.limit": "600", "clock.increment": "5", "color": "white",
	} {
		if got := strings.Join(form[k], ""); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
}

// Lichess has documented this response both flat and wrapped. Handling only one
// shape is an outage waiting for a deploy on their side.
func TestChallengeAcceptsTheWrappedShape(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"challenge":{"id":"wrapped9","url":"https://lichess.org/wrapped9"}}`))
	}))
	defer srv.Close()

	c := &lichess.Client{BaseURL: srv.URL}
	ch, err := c.Challenge(context.Background(), "tok", "someone", lichess.ChallengeParams{})
	if err != nil {
		t.Fatal(err)
	}
	if ch.ID != "wrapped9" {
		t.Fatalf("id = %q, want wrapped9", ch.ID)
	}
}

// A username is the one caller-supplied value that reaches a URL path here.
func TestChallengeRejectsABadUsernameBeforeSending(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("a request was sent for an invalid username")
	}))
	defer srv.Close()

	c := &lichess.Client{BaseURL: srv.URL}
	if _, err := c.Challenge(context.Background(), "tok", "../../admin", lichess.ChallengeParams{}); err == nil {
		t.Fatal("expected rejection")
	}
}

func TestStreamGameParsesFullThenStates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		// A blank keep-alive line and an unrelated event type are both part of
		// the real stream and must not derail parsing.
		for _, line := range []string{
			`{"type":"gameFull","id":"g1","state":{"type":"gameState","moves":"e2e4","status":"started"}}`,
			``,
			`{"type":"chatLine","text":"hello"}`,
			`{"type":"gameState","moves":"e2e4 e7e5","status":"started"}`,
			`{"type":"gameState","moves":"e2e4 e7e5 d1h5","status":"mate","winner":"white"}`,
		} {
			_, _ = w.Write([]byte(line + "\n"))
		}
	}))
	defer srv.Close()

	var got []lichess.GameState
	c := &lichess.Client{BaseURL: srv.URL}
	if err := c.StreamGame(context.Background(), "tok", "g1", func(s lichess.GameState) {
		got = append(got, s)
	}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d states, want 3 (chatLine and the keep-alive must be skipped)", len(got))
	}
	if got[0].Moves != "e2e4" {
		t.Errorf("first state moves = %q", got[0].Moves)
	}
	last := got[len(got)-1]
	if last.Status != "mate" || last.Winner != "white" {
		t.Errorf("final state = %+v", last)
	}
	if !lichess.Finished(last.Status) {
		t.Error("mate should be a finished status")
	}
}

func TestFinished(t *testing.T) {
	for status, want := range map[string]bool{
		"":          false,
		"created":   false,
		"started":   false,
		"mate":      true,
		"resign":    true,
		"draw":      true,
		"outoftime": true,
		"aborted":   true,
		"stalemate": true,
	} {
		if got := lichess.Finished(status); got != want {
			t.Errorf("Finished(%q) = %v, want %v", status, got, want)
		}
	}
}
