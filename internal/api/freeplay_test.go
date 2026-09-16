package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A real game: replaying to ply 88 leaves Black to move with b4d5 legal. The
// converter is asserted separately in internal/lichess; what matters here is
// that a fetched puzzle reaches a pupil.
const stubPGN = "e4 e5 Nf3 Nc6 Bc4 h6 d4 exd4 Nxd4 Nxd4 Qxd4 d6 Qd5 Be6 Qb5+ c6 Qb3 d5 exd5 Bxd5 O-O Bxc4 Qxc4 Be7 Re1 Qc7 Bf4 Qd7 Nc3 O-O-O Qe4 Bf6 Rad1 Qe6 Rxd8+ Bxd8 Qxe6+ fxe6 Rxe6 Nf6 f3 Bc7 Ne2 Bxf4 Nxf4 Rf8 Re7 g5 Ng6 Rd8 Re6 Nd5 c4 Nb4 Ne7+ Kd7 Re3 Re8 Nf5 Rxe3 Nxe3 Nxa2 Nf5 h5 Kf2 Ke6 Ng7+ Kf6 Nxh5+ Kf5 g4+ Kg6 Ng3 Nb4 Ke3 b5 b3 a5 cxb5 cxb5 f4 gxf4+ Kxf4 a4 bxa4 bxa4 Ne2 a3 Nc3"

// puzzleStub stands in for the Lichess puzzle API. The live one rate-limits,
// and a test that depends on someone else's service fails for reasons that have
// nothing to do with this code.
type puzzleStub struct {
	srv    *httptest.Server
	hits   int
	rating int
	// Each response needs a distinct id, or the second is ignored as a
	// duplicate and the bank never grows.
	served int
}

func newPuzzleStub(t *testing.T, rating int) *puzzleStub {
	t.Helper()
	s := &puzzleStub{rating: rating}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.hits++
		s.served++
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"game": map[string]any{"pgn": stubPGN},
			"puzzle": map[string]any{
				"id":         "STUB" + string(rune('A'+s.served)),
				"rating":     s.rating,
				"solution":   []string{"b4d5", "c3d5", "a3a2", "d5c3", "a2a1q"},
				"themes":     []string{"endgame"},
				"initialPly": 88,
			},
		})
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func freePlay(t *testing.T, c *client, tier string) (int, map[string]any) {
	t.Helper()
	status, obj, _ := c.do("GET", "/api/v1/puzzles/free?tier="+tier, nil)
	return status, obj
}

// The local bank serves the tier while it can.
func TestFreePlayServesAPuzzleFromTheBank(t *testing.T) {
	c := &client{t: t, srv: newServer(t)}
	c.login("penny@jca.ac.th")

	status, body := freePlay(t, c, "beginner")
	if status != 200 {
		t.Fatalf("status %d (%v)", status, body)
	}
	p, ok := body["puzzle"].(map[string]any)
	if !ok {
		t.Fatalf("no puzzle in %v", body)
	}
	if p["fen"] == "" || p["puzzleId"] == "" {
		t.Fatalf("incomplete puzzle: %v", p)
	}
	// The solution is the one thing that must never travel.
	if _, leaked := p["moves"]; leaked {
		t.Fatal("the solution was sent to the client")
	}
	if r, _ := p["rating"].(float64); r >= 800 {
		t.Fatalf("beginner tier served a %v-rated puzzle", r)
	}
}

// Free Play and the daily set share one bank under one rule: a puzzle a pupil
// has seen anywhere is spent. Repeating one would let a child farm the same
// position for practice credit.
func TestFreePlayNeverRepeatsAPuzzle(t *testing.T) {
	c := &client{t: t, srv: newServer(t)}
	c.login("penny@jca.ac.th")

	seen := map[string]bool{}
	for i := 0; i < 4; i++ {
		status, body := freePlay(t, c, "intermediate")
		if status != 200 {
			t.Fatalf("status %d (%v)", status, body)
		}
		p, ok := body["puzzle"].(map[string]any)
		if !ok {
			break // the tier ran out, which is its own answer
		}
		id, _ := p["puzzleId"].(string)
		if seen[id] {
			t.Fatalf("puzzle %s handed out twice", id)
		}
		seen[id] = true
	}
	if len(seen) < 3 {
		t.Fatalf("served only %d puzzles; not exercising repeats", len(seen))
	}
}

// The point of fetching on demand: a tier the bank cannot serve still hands the
// child a puzzle rather than an apology.
func TestAnEmptyTierIsRefilledFromLichess(t *testing.T) {
	stub := newPuzzleStub(t, 640) // inside the beginner band
	t.Setenv("LICHESS_API_BASE", stub.srv.URL)

	c := &client{t: t, srv: newServer(t)}
	c.login("penny@jca.ac.th")

	// Six seeded beginner puzzles, then the seventh has to come from Lichess.
	for i := 0; i < 7; i++ {
		freePlay(t, c, "beginner")
	}
	if stub.hits == 0 {
		t.Fatal("the emptied tier was never topped up from Lichess")
	}

	status, body := freePlay(t, c, "beginner")
	if status != 200 {
		t.Fatalf("status %d (%v)", status, body)
	}
	if body["exhausted"] == true {
		t.Fatal("tier reported empty while Lichess was answering")
	}
}

// A child on a run must not become an unbounded loop against someone else's
// API. Lichess rate-limits, and hammering through a 429 is how an integration
// gets blocked outright.
func TestToppingUpIsBounded(t *testing.T) {
	stub := newPuzzleStub(t, 1900) // never inside the beginner band
	t.Setenv("LICHESS_API_BASE", stub.srv.URL)

	c := &client{t: t, srv: newServer(t)}
	c.login("penny@jca.ac.th")

	for i := 0; i < 7; i++ {
		freePlay(t, c, "beginner")
	}
	before := stub.hits

	status, body := freePlay(t, c, "beginner")
	if status != 200 {
		t.Fatalf("status %d (%v)", status, body)
	}
	if body["exhausted"] != true {
		t.Fatalf("expected an unfillable tier to report itself empty, got %v", body)
	}
	if spent := stub.hits - before; spent > 3 {
		t.Fatalf("one press spent %d calls on Lichess", spent)
	}
}

func TestFreePlayRejectsAnUnknownTierAndAnonymousCallers(t *testing.T) {
	c := &client{t: t, srv: newServer(t)}
	c.login("penny@jca.ac.th")

	if status, _, _ := c.do("GET", "/api/v1/puzzles/free?tier=wizard", nil); status != 400 {
		t.Fatalf("unknown tier answered %d, wanted 400", status)
	}

	anon := &client{t: t, srv: c.srv}
	if status, _, _ := anon.do("GET", "/api/v1/puzzles/free?tier=beginner", nil); status != 401 {
		t.Fatalf("anonymous caller answered %d, wanted 401", status)
	}
}

// Free Play writes a puzzle_attempt like the daily set does, and the daily
// endpoint reads every attempt dated today. Without the `source` column that
// made a Free Play puzzle appear as a fourth puzzle in a set of three — which
// is what a child saw before migration 0034.
func TestFreePlayDoesNotJoinTodaysDailySet(t *testing.T) {
	c := &client{t: t, srv: newServer(t)}
	c.login("penny@jca.ac.th")

	status, daily, _ := c.do("GET", "/api/v1/puzzles/daily", nil)
	if status != 200 {
		t.Fatalf("daily: %d", status)
	}
	before := len(daily["puzzles"].([]any))

	for i := 0; i < 3; i++ {
		if s, body := freePlay(t, c, "beginner"); s != 200 {
			t.Fatalf("free play: %d (%v)", s, body)
		}
	}

	status, daily, _ = c.do("GET", "/api/v1/puzzles/daily", nil)
	if status != 200 {
		t.Fatalf("daily: %d", status)
	}
	after := len(daily["puzzles"].([]any))
	if after != before {
		t.Fatalf("the daily set grew from %d to %d puzzles after Free Play", before, after)
	}
	if after != 3 {
		t.Fatalf("the daily set holds %d puzzles, wanted 3", after)
	}
}
