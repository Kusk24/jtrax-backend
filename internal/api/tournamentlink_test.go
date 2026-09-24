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

// The console reads the current link when the Results tab opens, so it must be
// answerable without costing chess-results.com a request.
func TestReadingTheLinkDoesNotFetch(t *testing.T) {
	c, stub := newCRServer(t)
	c.login("admin@jca.ac.th")
	_, tour, _ := c.do("POST", "/api/v1/tournaments", map[string]any{"name": "Event"})
	id := tour["tournament_id"].(string)

	// Unlinked reads as a fact, not a failure.
	status, out, _ := c.do("GET", "/api/v1/tournaments/"+id+"/chess-results", nil)
	if status != 200 || out["linked"] != false {
		t.Fatalf("unlinked: want 200 linked=false, got %d (%v)", status, out)
	}

	c.do("POST", "/api/v1/tournaments/"+id+"/chess-results",
		map[string]any{"url": "https://chess-results.com/tnr123456.aspx?lan=1"})
	visitsAfterLink := stub.visits.Load()

	for i := 0; i < 3; i++ {
		status, out, _ = c.do("GET", "/api/v1/tournaments/"+id+"/chess-results", nil)
		if status != 200 || out["source"] != "chess-results" {
			t.Fatalf("linked read %d: %d (%v)", i, status, out)
		}
	}
	if got := stub.visits.Load(); got != visitsAfterLink {
		t.Fatalf("reading the link hit chess-results %d extra times", got-visitsAfterLink)
	}
}

func TestReadingTheLinkIsStaffOnly(t *testing.T) {
	c, _ := newCRServer(t)
	c.login("admin@jca.ac.th")
	_, tour, _ := c.do("POST", "/api/v1/tournaments", map[string]any{"name": "Event"})
	id := tour["tournament_id"].(string)

	anon := &client{t: t, srv: c.srv}
	if status, _, _ := anon.do("GET", "/api/v1/tournaments/"+id+"/chess-results", nil); status != 401 {
		t.Errorf("anonymous: want 401, got %d", status)
	}
	parent := &client{t: t, srv: c.srv}
	parent.login("sandy01234@gmail.com")
	if status, _, _ := parent.do("GET", "/api/v1/tournaments/"+id+"/chess-results", nil); status != 403 {
		t.Errorf("parent: want 403, got %d", status)
	}
}

/* One age group, one chess-results event.
 *
 * 0015 assumed a tournament is one event over there. A JCA chessfest runs
 * OPEN, U18, U12, U10 and U08 on the same day and the arbiter publishes each
 * separately — so under that assumption the console could follow exactly one
 * of them and "the results" meant whichever group somebody pasted first.
 */

// categoryOf adds a category to a tournament and returns its id.
func categoryOf(t *testing.T, c *client, tournamentID, name string) string {
	t.Helper()
	status, out, _ := c.do("POST", "/api/v1/tournament-categories",
		map[string]any{"tournament_id": tournamentID, "name": name})
	if status != 201 {
		t.Fatalf("create category %s: %d (%v)", name, status, out)
	}
	return out["tournament_category_id"].(string)
}

func TestACategoryCarriesItsOwnChessResultsEvent(t *testing.T) {
	c, id := linkedEvent(t)
	u12 := categoryOf(t, c, id, "U12 Junior")

	// Not linked is a fact the card renders an empty state from, not an error.
	status, out, _ := c.do("GET", "/api/v1/tournaments/categories/"+u12+"/chess-results", nil)
	if status != 200 || out["linked"] != false {
		t.Fatalf("before linking: %d (%v)", status, out)
	}

	status, out, _ = c.do("POST", "/api/v1/tournaments/categories/"+u12+"/chess-results",
		map[string]any{"url": "https://chess-results.com/tnr123456.aspx?lan=1"})
	if status != 200 {
		t.Fatalf("link the group: %d (%v)", status, out)
	}
	standings, _ := out["standings"].([]any)
	if len(standings) != 3 {
		t.Fatalf("want the arbiter's rows for this group, got %d", len(standings))
	}

	status, out, _ = c.do("GET", "/api/v1/tournaments/categories/"+u12+"/chess-results", nil)
	if status != 200 || out["chessResultsId"] != float64(123456) {
		t.Fatalf("after linking: %d (%v)", status, out)
	}
}

// Each group keeps its own link. The bug this guards is one group's link
// overwriting another's, which is what a single column per tournament did.
func TestTwoCategoriesKeepSeparateLinks(t *testing.T) {
	c, id := linkedEvent(t)
	u12 := categoryOf(t, c, id, "U12 Junior")
	u08 := categoryOf(t, c, id, "U08 Junior")

	c.do("POST", "/api/v1/tournaments/categories/"+u12+"/chess-results",
		map[string]any{"url": "https://chess-results.com/tnr123456.aspx?lan=1"})
	c.do("POST", "/api/v1/tournaments/categories/"+u08+"/chess-results",
		map[string]any{"url": "https://chess-results.com/tnr123456.aspx?lan=1"})

	// Unlinking one must not touch the other.
	if status, _, _ := c.do("DELETE", "/api/v1/tournaments/categories/"+u12+"/chess-results", nil); status != 200 {
		t.Fatalf("unlink u12: %d", status)
	}
	_, out, _ := c.do("GET", "/api/v1/tournaments/categories/"+u12+"/chess-results", nil)
	if out["linked"] != false {
		t.Errorf("u12 should be unlinked, got %v", out)
	}
	_, out, _ = c.do("GET", "/api/v1/tournaments/categories/"+u08+"/chess-results", nil)
	if out["chessResultsId"] != float64(123456) {
		t.Errorf("u08 should still be linked, got %v", out)
	}
}

// The tournament-level link still works: some events really are one list, and
// nothing had to be migrated.
func TestTheWholeTournamentCanStillBeLinked(t *testing.T) {
	c, id := linkedEvent(t)
	_, out, _ := c.do("GET", "/api/v1/tournaments/"+id+"/chess-results", nil)
	if out["chessResultsId"] != float64(123456) {
		t.Fatalf("the tournament's own link should be untouched, got %v", out)
	}
}

// Only chess-results.com, on a category too — this is a server-side fetch of a
// URL the caller supplies, and the host check is what stops it being a way to
// point the server anywhere.
func TestACategoryRefusesAForeignLink(t *testing.T) {
	c, id := linkedEvent(t)
	u12 := categoryOf(t, c, id, "U12 Junior")
	for _, bad := range []string{
		"https://example.com/tnr1.aspx", "http://localhost:8080/admin", "not a url",
	} {
		status, _, _ := c.do("POST", "/api/v1/tournaments/categories/"+u12+"/chess-results",
			map[string]any{"url": bad})
		if status != 400 {
			t.Errorf("%s: want 400, got %d", bad, status)
		}
	}
}

func TestCategoryLinkingIsStaffOnly(t *testing.T) {
	c, id := linkedEvent(t)
	u12 := categoryOf(t, c, id, "U12 Junior")

	for _, email := range []string{"sandy01234@gmail.com", "penny@jca.ac.th"} {
		other := &client{t: t, srv: c.srv}
		other.login(email)
		if status, _, _ := other.do("POST", "/api/v1/tournaments/categories/"+u12+"/chess-results",
			map[string]any{"url": "https://chess-results.com/tnr123456.aspx?lan=1"}); status != 403 {
			t.Errorf("%s linking: want 403, got %d", email, status)
		}
		if status, _, _ := other.do("GET", "/api/v1/tournaments/categories/"+u12+"/chess-results", nil); status != 403 {
			t.Errorf("%s reading: want 403, got %d", email, status)
		}
	}
}

// One link, several age groups.
//
// An arbiter can publish a group event either as separate tournaments — which
// is what a per-category link is for — or as one tournament with each player's
// group in the ranking table's "Typ" column. tnr1193905, "WCIB CHESS
// CHAMPIONSHIP 2025 [U14 + G14]", is the second kind: there is one link to
// give, so no amount of per-category linking can divide it.
//
// The group has to survive the whole way — parsed, stored, and served — or the
// Results tab has nothing to build its tabs from.
func TestOneLinkCarriesEveryGroupItPublishes(t *testing.T) {
	c, id := linkedEvent(t)

	status, out, _ := c.do("GET", "/api/v1/tournaments/"+id+"/chess-results", nil)
	if status != 200 {
		t.Fatalf("read link: %d (%v)", status, out)
	}
	rows, _ := out["standings"].([]any)
	if len(rows) == 0 {
		t.Fatal("no standings came back")
	}
	seen := map[string]bool{}
	for _, r := range rows {
		row, _ := r.(map[string]any)
		if s, _ := row["type"].(string); s != "" {
			seen[s] = true
		}
	}
	if !seen["U14"] || !seen["G14"] {
		t.Errorf("groups reaching the console = %v, want both U14 and G14", seen)
	}
}

// Families see which section a child played in, because it is already on the
// wall at the venue. It travels on the public feed for the same reason the
// tab strip exists: an undivided list of every group is not a result anyone
// can read.
func TestThePublicResultsNameTheGroupToo(t *testing.T) {
	c, id := linkedEvent(t)

	pub := &client{t: t, srv: c.srv}
	status, out, _ := pub.do("GET", "/api/v1/public/tournaments/"+id+"/results", nil)
	if status != 200 {
		t.Fatalf("public results: %d (%v)", status, out)
	}
	rows, _ := out["standings"].([]any)
	if len(rows) == 0 {
		t.Fatal("no standings came back")
	}
	first, _ := rows[0].(map[string]any)
	if first["type"] != "U14" {
		t.Errorf("first public row type = %v, want U14", first["type"])
	}
	// Still nothing about who is ours — the group is a section, not a child.
	if _, leaked := first["studentId"]; leaked {
		t.Error("the public feed named one of our students")
	}
}
