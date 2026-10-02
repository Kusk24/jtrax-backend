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

// A pupil opening Puzzles must not become an unbounded loop against someone
// else's API: when Lichess keeps sending puzzles outside the level asked for,
// one request stops after its cap of calls.
func TestFillingTheListCallsLichessABoundedNumberOfTimes(t *testing.T) {
	stub := newPuzzleStub(t, 1900) // never inside the beginner band
	t.Setenv("LICHESS_API_BASE", stub.srv.URL)
	d := newDB(t)
	setLevel(t, d, "penny@jca.ac.th", "Beginner")
	c := &client{t: t, srv: newServerOn(t, d)}
	c.login("penny@jca.ac.th")

	puzzleList(t, c)
	if stub.hits > 12 {
		t.Fatalf("one request spent %d calls on Lichess, cap is 12", stub.hits)
	}
}
