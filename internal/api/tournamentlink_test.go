package api_test

import (
	"testing"
)

/* Linking one of the academy's tournaments to the event an arbiter publishes. */

// linkedEvent creates a published tournament and points it at the stub's
// chess-results event. Returns the client and the tournament id.
func linkedEvent(t *testing.T) (*client, string) {
	t.Helper()
	c, _ := newCRServer(t)
	c.login("admin@jca.ac.th")

	status, tour, _ := c.do("POST", "/api/v1/tournaments", map[string]any{
		"name": "JCA at the Bangkok Open", "results_public": true,
	})
	if status != 201 {
		t.Fatalf("create tournament: %d (%v)", status, tour)
	}
	id := tour["tournament_id"].(string)

	status, out, _ := c.do("POST", "/api/v1/tournaments/"+id+"/chess-results",
		map[string]any{"url": "https://chess-results.com/tnr123456.aspx?lan=1"})
	if status != 200 {
		t.Fatalf("link: %d (%v)", status, out)
	}
	return c, id
}

// The point of the whole feature: once linked, the page a parent opens shows
// the arbiter's table, not one typed into the console.
func TestPublicResultsFollowTheLinkedEvent(t *testing.T) {
	c, id := linkedEvent(t)

	pub := &client{t: t, srv: c.srv}
	status, out, _ := pub.do("GET", "/api/v1/public/tournaments/"+id+"/results", nil)
	if status != 200 {
		t.Fatalf("public results: %d (%v)", status, out)
	}
	if out["source"] != "chess-results" {
		t.Fatalf("want the chess-results source, got %v", out["source"])
	}
	if out["sourceUrl"] == "" || out["sourceUrl"] == nil {
		t.Fatalf("a page served from a cache of someone else's site must link back to it")
	}
	standings, _ := out["standings"].([]any)
	if len(standings) != 3 {
		t.Fatalf("want the arbiter's 3 rows, got %d (%v)", len(standings), standings)
	}
	top := standings[0].(map[string]any)
	if top["name"] != "Somchai, Niran" || top["points"] != float64(4) {
		t.Fatalf("standings are not the arbiter's: %v", top)
	}
}

// The academy's own knowledge of which rows are its pupils must not ride along
// on a page with no sign-in.
func TestPublicResultsHideWhichPlayersAreOurs(t *testing.T) {
	c, id := linkedEvent(t)

	pub := &client{t: t, srv: c.srv}
	_, out, _ := pub.do("GET", "/api/v1/public/tournaments/"+id+"/results", nil)
	for _, raw := range out["standings"].([]any) {
		row := raw.(map[string]any)
		for _, leak := range []string{"studentId", "studentName"} {
			if _, found := row[leak]; found {
				t.Fatalf("public standings carry %q: %v", leak, row)
			}
		}
	}
}

// Unpublished stays unpublished: linking an event must not become a back door
// around results_public.
func TestLinkedResultsStillRequirePublishing(t *testing.T) {
	c, _ := newCRServer(t)
	c.login("admin@jca.ac.th")
	_, tour, _ := c.do("POST", "/api/v1/tournaments", map[string]any{
		"name": "Private", "results_public": false,
	})
	id := tour["tournament_id"].(string)
	c.do("POST", "/api/v1/tournaments/"+id+"/chess-results",
		map[string]any{"url": "https://chess-results.com/tnr123456.aspx?lan=1"})

	pub := &client{t: t, srv: c.srv}
	if status, _, _ := pub.do("GET", "/api/v1/public/tournaments/"+id+"/results", nil); status != 404 {
		t.Fatalf("unpublished but linked: want 404, got %d", status)
	}
}

func TestUnlinkingRestoresOurOwnStandings(t *testing.T) {
	c, id := linkedEvent(t)

	if status, _, _ := c.do("DELETE", "/api/v1/tournaments/"+id+"/chess-results", nil); status != 200 {
		t.Fatalf("unlink failed")
	}
	pub := &client{t: t, srv: c.srv}
	_, out, _ := pub.do("GET", "/api/v1/public/tournaments/"+id+"/results", nil)
	if out["source"] == "chess-results" {
		t.Fatalf("still following the arbiter after unlinking")
	}
	// Back to the console's own table, which for a fresh tournament is empty
	// rather than absent.
	if _, ok := out["standings"].([]any); !ok {
		t.Fatalf("want our own standings back, got %v", out["standings"])
	}
}

func TestLinkingIsStaffOnlyAndRefusesForeignHosts(t *testing.T) {
	c, _ := newCRServer(t)
	c.login("admin@jca.ac.th")
	_, tour, _ := c.do("POST", "/api/v1/tournaments", map[string]any{"name": "Event"})
	id := tour["tournament_id"].(string)

	// A link to anywhere else must be refused: this endpoint decides what the
	// server fetches, and that is the whole of the SSRF surface.
	for _, bad := range []string{
		"https://example.com/tnr1.aspx",
		"http://169.254.169.254/latest/meta-data/",
		"not a url at all",
	} {
		if status, _, _ := c.do("POST", "/api/v1/tournaments/"+id+"/chess-results",
			map[string]any{"url": bad}); status != 400 {
			t.Errorf("%s: want 400, got %d", bad, status)
		}
	}

	anon := &client{t: t, srv: c.srv}
	if status, _, _ := anon.do("POST", "/api/v1/tournaments/"+id+"/chess-results",
		map[string]any{"url": "https://chess-results.com/tnr123456.aspx"}); status != 401 {
		t.Errorf("anonymous link: want 401, got %d", status)
	}
	parent := &client{t: t, srv: c.srv}
	parent.login("sandy01234@gmail.com")
	if status, _, _ := parent.do("POST", "/api/v1/tournaments/"+id+"/chess-results",
		map[string]any{"url": "https://chess-results.com/tnr123456.aspx"}); status != 403 {
		t.Errorf("parent link: want 403, got %d", status)
	}
}
