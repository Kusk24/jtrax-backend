package api_test

import (
	"database/sql"
	"testing"
	"time"
)

/* Rounds from chess-results: the pairing pages the arbiter uploads before and
   after every round, mirrored so a parent can see which board their child is
   on without hammering the arbiter's site. */

// linkedRoundsEvent is linkedEvent with the stub and database exposed, for
// tests that age the copy or move the arbiter's site on.
func linkedRoundsEvent(t *testing.T) (*client, string, *crStub, *sql.DB) {
	t.Helper()
	stub := newCRStub(t)
	t.Setenv("CHESS_RESULTS_API_BASE", stub.srv.URL)
	d := newDB(t)
	c := &client{t: t, srv: newServerOn(t, d)}
	c.login("admin@jca.ac.th")
	status, tour, _ := c.do("POST", "/api/v1/tournaments", map[string]any{
		"name": "JCA at the Bangkok Open", "results_public": true,
	})
	if status != 201 {
		t.Fatalf("create tournament: %d", status)
	}
	id := tour["tournament_id"].(string)
	if status, out, _ := c.do("POST", "/api/v1/tournaments/"+id+"/chess-results",
		map[string]any{"url": "https://chess-results.com/tnr123456.aspx?lan=1"}); status != 200 {
		t.Fatalf("link: %d (%v)", status, out)
	}
	return c, id, stub, d
}

func TestLinkingStoresTheRoundsToo(t *testing.T) {
	c, id := linkedEvent(t) // stub stage: "Rank after Round 4"

	pub := &client{t: t, srv: c.srv}
	status, out, _ := pub.do("GET", "/api/v1/public/tournaments/"+id+"/results", nil)
	if status != 200 {
		t.Fatalf("public results: %d", status)
	}
	rounds, _ := out["rounds"].([]any)
	// Rounds 1..4 are played; round 5's pairings are already published.
	if len(rounds) != 5 {
		t.Fatalf("rounds = %d, want 5 (%v)", len(rounds), out["rounds"])
	}
	last := rounds[4].(map[string]any)
	if last["status"] != "pending" || last["round"] != float64(5) {
		t.Fatalf("round 5 should be the pending one: %v", last)
	}
	pairings := last["pairings"].([]any)
	top := pairings[0].(map[string]any)
	if top["white"] != "Somchai, Niran" || top["black"] != "Stranger, Alice" {
		t.Fatalf("board one of the pending round: %v", top)
	}
	if r, _ := top["result"].(string); r != "" {
		t.Fatalf("a round that has not been played carries a result: %v", top)
	}
	third := rounds[2].(map[string]any)
	if third["status"] != "played" {
		t.Fatalf("round 3 should be played: %v", third)
	}
	if res := third["pairings"].([]any)[0].(map[string]any)["result"]; res != "1 - 0" {
		t.Fatalf("round 3 board 1 result = %v", res)
	}
}

// The public page must not say which seats are the academy's pupils — same
// rule the standings already follow.
func TestPublicRoundsHideWhichSeatsAreOurs(t *testing.T) {
	c, id := linkedEvent(t)

	pub := &client{t: t, srv: c.srv}
	_, out, _ := pub.do("GET", "/api/v1/public/tournaments/"+id+"/results", nil)
	for _, r := range out["rounds"].([]any) {
		for _, b := range r.(map[string]any)["pairings"].([]any) {
			board := b.(map[string]any)
			for key := range board {
				switch key {
				case "board", "white", "whiteRating", "black", "blackRating", "result":
				default:
					t.Fatalf("public board carries %q: %v", key, board)
				}
			}
		}
	}

	// The staff view, by contrast, is exactly where that knowledge belongs.
	status, detail, _ := c.do("GET", "/api/v1/tournaments/"+id+"/chess-results", nil)
	if status != 200 {
		t.Fatalf("staff link view: %d", status)
	}
	found := false
	for _, r := range detail["rounds"].([]any) {
		for _, b := range r.(map[string]any)["pairings"].([]any) {
			if b.(map[string]any)["whiteStudentName"] == "Penny" ||
				b.(map[string]any)["blackStudentName"] == "Penny" {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("the staff view never recognised Penny on a board")
	}
}

// A counted round is immutable and must never be refetched; the pending round
// is refetched and promoted when the ranking counts it.
func TestACountedRoundIsFetchedOnce(t *testing.T) {
	_, id, stub, d := linkedRoundsEvent(t)

	// The link itself claimed the politeness floor; a restart forgets the
	// in-memory throttle (the deps say so), which is what lets this test move
	// time forward without waiting a real minute.
	c := &client{t: t, srv: newServerOn(t, d)}
	c.login("admin@jca.ac.th")

	before := stub.visits.Load()
	// The arbiter uploads round 5's results: the ranking heading moves on.
	stub.stage.Store("Rank after Round 5")
	status, _, _ := c.do("POST", "/api/v1/tournaments/"+id+"/chess-results/refresh", nil)
	if status != 200 {
		t.Fatalf("refresh: %d", status)
	}
	// One ranking page, maybe its player list, round 5 again (it was pending),
	// and the round 6 probe. Rounds 1..4 must not be among the fetches.
	if got := stub.visits.Load() - before; got > 4 {
		t.Fatalf("refresh cost %d fetches — the played rounds are being refetched", got)
	}

	pub := &client{t: t, srv: c.srv}
	_, out, _ := pub.do("GET", "/api/v1/public/tournaments/"+id+"/results", nil)
	rounds := out["rounds"].([]any)
	if len(rounds) != 6 {
		t.Fatalf("rounds = %d, want 6 after the arbiter's upload", len(rounds))
	}
	fifth := rounds[4].(map[string]any)
	if fifth["status"] != "played" {
		t.Fatalf("round 5 was counted by the ranking but stayed pending: %v", fifth)
	}
	if res := fifth["pairings"].([]any)[0].(map[string]any)["result"]; res != "1 - 0" {
		t.Fatalf("round 5's results did not arrive: %v", res)
	}
}

// A public read of a stale, unfinished event refreshes the copy in the
// background — the academy's workflow never includes touching JTrax between
// rounds, so nobody else will.
func TestAStalePublicReadRefreshesByItself(t *testing.T) {
	_, id, stub, d := linkedRoundsEvent(t)
	c := &client{t: t, srv: newServerOn(t, d)} // fresh throttle, same data

	// Age the copy past the live interval, then move the arbiter's site on.
	if _, err := d.Exec(
		`UPDATE external_tournament SET fetched_at = datetime('now','-10 minutes')`); err != nil {
		t.Fatal(err)
	}
	stub.stage.Store("Rank after Round 5")

	pub := &client{t: t, srv: c.srv}
	status, out, _ := pub.do("GET", "/api/v1/public/tournaments/"+id+"/results", nil)
	if status != 200 {
		t.Fatalf("public read: %d", status)
	}
	// The read itself serves the stored copy — round 5 still pending — because
	// a parent's phone must not wait on chess-results.com.
	if rounds := out["rounds"].([]any); len(rounds) != 5 {
		t.Fatalf("the read should serve the stored copy, got %d rounds", len(rounds))
	}

	// The background refresh lands shortly after.
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, out, _ = pub.do("GET", "/api/v1/public/tournaments/"+id+"/results", nil)
		if len(out["rounds"].([]any)) == 6 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the copy never caught up with the arbiter: %d rounds", len(out["rounds"].([]any)))
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// Once the ranking says Final, the event never costs the arbiter's site
// another request, however often the public page is read.
func TestAFinishedEventIsNeverRefetched(t *testing.T) {
	_, id, stub, d := linkedRoundsEvent(t)
	c := &client{t: t, srv: newServerOn(t, d)} // fresh throttle, same data

	stub.stage.Store("Final Ranking after 5 Rounds")
	c.login("admin@jca.ac.th")
	if status, _, _ := c.do("POST", "/api/v1/tournaments/"+id+"/chess-results/refresh", nil); status != 200 {
		t.Fatal("refresh to final failed")
	}
	// Stale by every clock, but final.
	if _, err := d.Exec(
		`UPDATE external_tournament SET fetched_at = datetime('now','-2 days')`); err != nil {
		t.Fatal(err)
	}

	before := stub.visits.Load()
	pub := &client{t: t, srv: c.srv}
	for i := 0; i < 3; i++ {
		if status, _, _ := pub.do("GET", "/api/v1/public/tournaments/"+id+"/results", nil); status != 200 {
			t.Fatal("public read failed")
		}
	}
	time.Sleep(200 * time.Millisecond) // room for any background fetch to fire
	if got := stub.visits.Load() - before; got != 0 {
		t.Fatalf("a finished event cost the site %d fetches", got)
	}
	// And the phantom pending round is gone: final means the table ends.
	_, out, _ := pub.do("GET", "/api/v1/public/tournaments/"+id+"/results", nil)
	if rounds := out["rounds"].([]any); len(rounds) != 5 {
		t.Fatalf("a final event still lists %d rounds, want 5", len(rounds))
	}
}

// The portals' discovery question: is there a tournament to follow right now?
func TestLiveTournamentListShowsOnlyPublishedUnfinishedEvents(t *testing.T) {
	base := &client{t: t, srv: newServer(t)}
	base.login("admin@jca.ac.th")
	mk := func(name, status string, public bool) {
		if st, out, _ := base.do("POST", "/api/v1/tournaments", map[string]any{
			"name": name, "tournament_status": status, "results_public": public,
		}); st != 201 {
			t.Fatalf("create %s: %d (%v)", name, st, out)
		}
	}
	mk("Ongoing & public", "Ongoing", true)
	mk("Upcoming & public", "Upcoming", true)
	mk("Finished & public", "Completed", true)
	mk("Ongoing but private", "Ongoing", false)

	pub := &client{t: t, srv: base.srv}
	status, _, list := pub.do("GET", "/api/v1/public/live-tournaments", nil)
	if status != 200 {
		t.Fatalf("live list: %d", status)
	}
	names := make([]string, len(list))
	for i, e := range list {
		names[i] = e["name"].(string)
	}
	if len(names) != 2 || names[0] != "Ongoing & public" || names[1] != "Upcoming & public" {
		t.Fatalf("live list = %v — finished and private events must not appear, ongoing leads", names)
	}
	for _, e := range list {
		for key := range e {
			switch key {
			case "tournamentId", "name", "status":
			default:
				t.Fatalf("live list leaks %q: %v", key, e)
			}
		}
	}
}
