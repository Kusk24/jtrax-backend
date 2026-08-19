package api_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

/* A stand-in for chess-results.com serving a synthetic but faithfully-shaped
   tournament: the CRs1 table class, the two-heading layout, comma decimals,
   and a FideID column only on the player list — every quirk the real parser
   has to survive, in miniature. */

type crStub struct {
	srv    *httptest.Server
	visits atomic.Int64
	// stage is mutable so a test can move a tournament from round 4 to final.
	stage atomic.Value
}

func newCRStub(t *testing.T) *crStub {
	s := &crStub{}
	s.stage.Store("Rank after Round 4")
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.visits.Add(1)
		switch r.URL.Query().Get("art") {
		case "1":
			fmt.Fprintf(w, `<h2>Bangkok Open 2026</h2><h2>%s</h2>
			<table class="CRs1">
			<tr><th>Rk.</th><th>Name</th><th>FED</th><th>Rtg</th><th>Pts.</th></tr>
			<tr><td>1</td><td>Somchai, Niran</td><td>THA</td><td>1650</td><td>4</td></tr>
			<tr><td>2</td><td>Penny</td><td>THA</td><td>1200</td><td>3,5</td></tr>
			<tr><td>3</td><td>Stranger, Alice</td><td>SGP</td><td>1400</td><td>2</td></tr>
			</table>`, s.stage.Load())
		case "3":
			fmt.Fprint(w, `<h2>Bangkok Open 2026</h2><h2>Alphabetical list</h2>
			<table class="CRs1">
			<tr><th>No.</th><th>Name</th><th>FideID</th><th>Rtg</th></tr>
			<tr><td>1</td><td>Somchai, Niran</td><td>61234567</td><td>1650</td></tr>
			<tr><td>2</td><td>Penny</td><td></td><td>1200</td></tr>
			<tr><td>3</td><td>Stranger, Alice</td><td>5800000</td><td>1400</td></tr>
			</table>`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func newCRServer(t *testing.T) (*client, *crStub) {
	t.Helper()
	stub := newCRStub(t)
	t.Setenv("CHESS_RESULTS_API_BASE", stub.srv.URL)
	return &client{t: t, srv: newServer(t)}, stub
}

func trackAsAdmin(t *testing.T, base *client, url string) map[string]any {
	t.Helper()
	admin := asStudent(t, base, "admin@jca.ac.th")
	status, obj, _ := admin.do("POST", "/api/v1/external-tournaments", map[string]string{"url": url})
	if status != 201 && status != 200 {
		t.Fatalf("track: status %d (%v)", status, obj)
	}
	return obj
}

/* ---- tracking ---- */

func TestTrackImportsStandingsAndRecognisesStudents(t *testing.T) {
	base, _ := newCRServer(t)

	// Give a student a FIDE ID first, so both match paths are exercised —
	// and register them under a name the tournament does NOT use, so only the
	// FIDE ID can make the join. A spelling that also matches by name would
	// let the name fallback quietly carry a broken FIDE path.
	admin := asStudent(t, base, "admin@jca.ac.th")
	status, created, _ := admin.do("POST", "/api/v1/students", map[string]any{
		"name": "Nino S.", "fide_id": "61234567",
	})
	if status != 201 {
		t.Fatalf("creating the FIDE-carrying student: %d (%v)", status, created)
	}

	view := trackAsAdmin(t, base, "https://chess-results.com/tnr123456.aspx?lan=1")
	if view["name"] != "Bangkok Open 2026" {
		t.Errorf("name = %v", view["name"])
	}
	if view["academyPlayers"] != float64(2) {
		t.Errorf("academyPlayers = %v, want 2 (one by FIDE ID, one by name)", view["academyPlayers"])
	}

	extID := view["externalTournamentId"].(string)
	status, detail, _ := admin.do("GET", "/api/v1/external-tournaments/"+extID, nil)
	if status != 200 {
		t.Fatalf("get: %d", status)
	}
	standings := detail["standings"].([]any)
	if len(standings) != 3 {
		t.Fatalf("standings rows = %d, want 3", len(standings))
	}

	first := standings[0].(map[string]any)
	// "Somchai, Niran" in the arbiter's table is our "Nino S.": only the
	// FIDE ID can make that join, which is why the column exists.
	if first["studentName"] != "Nino S." {
		t.Errorf("rank 1 not recognised by FIDE ID: %v", first)
	}
	second := standings[1].(map[string]any)
	if second["studentName"] != "Penny" {
		t.Errorf("rank 2 not recognised by name: %v", second)
	}
	if second["points"] != 3.5 {
		t.Errorf("points = %v, want 3.5 (comma decimal survived the trip)", second["points"])
	}
	third := standings[2].(map[string]any)
	if s, _ := third["studentId"].(string); s != "" {
		t.Errorf("a stranger was matched to a student: %v", third)
	}
}

func TestTrackRequiresStaff(t *testing.T) {
	base, _ := newCRServer(t)
	penny := asStudent(t, base, "penny@jca.ac.th")
	if status, _, _ := penny.do("POST", "/api/v1/external-tournaments",
		map[string]string{"url": "https://chess-results.com/tnr1.aspx"}); status != 403 {
		t.Fatalf("student track: %d, want 403", status)
	}
	sandy := asStudent(t, base, "sandy01234@gmail.com")
	if status, _, _ := sandy.do("POST", "/api/v1/external-tournaments",
		map[string]string{"url": "https://chess-results.com/tnr1.aspx"}); status != 403 {
		t.Fatalf("parent track: %d, want 403", status)
	}
}

// This endpoint turns caller input into a server-side GET. Only
// chess-results.com may ever be on the other end of it.
func TestTrackRefusesForeignURLs(t *testing.T) {
	base, stub := newCRServer(t)
	admin := asStudent(t, base, "admin@jca.ac.th")
	for _, u := range []string{
		"https://evil.example/tnr1.aspx",
		"https://169.254.169.254/latest/meta-data",
		"https://chess-results.com.evil.example/tnr1.aspx",
	} {
		if status, _, _ := admin.do("POST", "/api/v1/external-tournaments",
			map[string]string{"url": u}); status != 400 {
			t.Errorf("url %q: status %d, want 400", u, status)
		}
	}
	if stub.visits.Load() != 0 {
		t.Errorf("a refused URL still caused %d outbound fetches", stub.visits.Load())
	}
}

// A parent can read what staff track — following their child's tournament is
// the audience — but the data is served from our copy, not their server.
func TestParentReadsFromTheStoredCopy(t *testing.T) {
	base, stub := newCRServer(t)
	view := trackAsAdmin(t, base, "https://chess-results.com/tnr7.aspx")
	extID := view["externalTournamentId"].(string)
	before := stub.visits.Load()

	sandy := asStudent(t, base, "sandy01234@gmail.com")
	status, detail, _ := sandy.do("GET", "/api/v1/external-tournaments/"+extID, nil)
	if status != 200 {
		t.Fatalf("parent get: %d", status)
	}
	if len(detail["standings"].([]any)) != 3 {
		t.Errorf("parent sees %d rows", len(detail["standings"].([]any)))
	}
	// Fresh copy, unfinished event, read within the politeness floor: no new
	// fetch may have happened. Every parent refresh hitting their server is
	// exactly what the stored copy exists to prevent.
	if got := stub.visits.Load(); got != before {
		t.Errorf("a parent read caused %d extra outbound fetches", got-before)
	}
}

// The refresh button must not be able to hammer a donation-run site, however
// enthusiastically it is pressed.
func TestRefreshIsThrottledPerTournament(t *testing.T) {
	base, _ := newCRServer(t)
	view := trackAsAdmin(t, base, "https://chess-results.com/tnr9.aspx")
	extID := view["externalTournamentId"].(string)

	admin := asStudent(t, base, "admin@jca.ac.th")
	status, _, _ := admin.do("POST", "/api/v1/external-tournaments/"+extID+"/refresh", nil)
	if status != 429 {
		t.Fatalf("refresh straight after tracking: %d, want 429 (the track itself was a fetch)", status)
	}
}

func TestTrackingTwiceReturnsTheExistingRecord(t *testing.T) {
	base, _ := newCRServer(t)
	first := trackAsAdmin(t, base, "https://chess-results.com/tnr55.aspx")
	second := trackAsAdmin(t, base, "https://s2.chess-results.com/tnr55.aspx?lan=1&art=1")
	if first["externalTournamentId"] != second["externalTournamentId"] {
		t.Errorf("the same tournament was tracked twice: %v vs %v",
			first["externalTournamentId"], second["externalTournamentId"])
	}
}

func TestUntrackRemovesTheTournament(t *testing.T) {
	base, _ := newCRServer(t)
	view := trackAsAdmin(t, base, "https://chess-results.com/tnr77.aspx")
	extID := view["externalTournamentId"].(string)

	admin := asStudent(t, base, "admin@jca.ac.th")
	if status, _, _ := admin.do("DELETE", "/api/v1/external-tournaments/"+extID, nil); status != 200 {
		t.Fatalf("untrack: %d", status)
	}
	if status, _, _ := admin.do("GET", "/api/v1/external-tournaments/"+extID, nil); status != 404 {
		t.Fatalf("after untrack: %d, want 404", status)
	}
	penny := asStudent(t, base, "penny@jca.ac.th")
	if status, _, _ := penny.do("DELETE", "/api/v1/external-tournaments/"+extID, nil); status != 403 {
		t.Fatalf("student untrack: %d, want 403", status)
	}
}

// The list endpoint once deadlocked the entire server: it issued a query per
// row while the listing rows were still open, and the local database has a
// single connection. This test exists so that bug hangs a test run instead of
// a school. It lists *two* tournaments because one row happened to work.
func TestListExternalDoesNotHoldTheConnectionAcrossLoads(t *testing.T) {
	base, _ := newCRServer(t)
	trackAsAdmin(t, base, "https://chess-results.com/tnr301.aspx")
	trackAsAdmin(t, base, "https://chess-results.com/tnr302.aspx")

	admin := asStudent(t, base, "admin@jca.ac.th")
	status, _, list := admin.do("GET", "/api/v1/external-tournaments", nil)
	if status != 200 || len(list) != 2 {
		t.Fatalf("list: status %d, %d rows, want 200 with 2", status, len(list))
	}
}
