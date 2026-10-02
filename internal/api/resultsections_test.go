package api_test

import (
	"testing"
)

/* One pasted link connects a tournament to every category the arbiter
   published; those become its results categories, apart from the
   registration categories. */

func connectedTournament(t *testing.T) (*client, string, map[string]any) {
	t.Helper()
	c, _ := newCRServer(t)
	c.login("admin@jca.ac.th")
	_, tour, _ := c.do("POST", "/api/v1/tournaments", map[string]any{"name": "JCA Chessfest"})
	id := tour["tournament_id"].(string)
	status, out, _ := c.do("POST", "/api/v1/tournaments/"+id+"/results-sections",
		map[string]any{"url": "https://s2.chess-results.com/tnr123456.aspx?lan=1&art=2&rd=7&SNode=S0"})
	if status != 200 {
		t.Fatalf("connect: %d (%v)", status, out)
	}
	return c, id, out
}

func TestOneLinkConnectsEveryCategory(t *testing.T) {
	_, _, out := connectedTournament(t)

	if out["connected"] != true || out["eventName"] != "Bangkok Open 2026" || num(out, "rounds") != 7 {
		t.Fatalf("event: %v", out)
	}
	sections := out["sections"].([]any)
	want := []struct {
		id   int
		name string
	}{{123455, "U12 + G12"}, {123456, "U14 + G14"}, {123457, "U16"}}
	if len(sections) != len(want) {
		t.Fatalf("sections: %v", sections)
	}
	for i, w := range want {
		s := sections[i].(map[string]any)
		if num(s, "chessResultsId") != w.id || s["name"] != w.name {
			t.Fatalf("section %d: want %d %q, got %v", i, w.id, w.name, s)
		}
		// Every section's standings are read at connect time.
		if s["tracked"] != true || num(s, "players") != 3 {
			t.Fatalf("section %d was not read: %v", i, s)
		}
	}
}

// The arbiter's categories are kept apart from the ones families register
// in: connecting neither adds, renames nor removes a registration category.
func TestConnectingLeavesRegistrationCategoriesAlone(t *testing.T) {
	c, _ := newCRServer(t)
	c.login("admin@jca.ac.th")
	_, tour, _ := c.do("POST", "/api/v1/tournaments", map[string]any{"name": "JCA Chessfest"})
	id := tour["tournament_id"].(string)
	for _, name := range []string{"U8", "U10"} {
		if status, out, _ := c.do("POST", "/api/v1/tournament-categories",
			map[string]any{"tournament_id": id, "name": name}); status != 201 {
			t.Fatalf("category %s: %d (%v)", name, status, out)
		}
	}
	count := func() int {
		_, _, list := c.do("GET", "/api/v1/tournament-categories", nil)
		n := 0
		for _, row := range list {
			if row["tournament_id"] == id {
				n++
			}
		}
		return n
	}
	before := count()
	if status, out, _ := c.do("POST", "/api/v1/tournaments/"+id+"/results-sections",
		map[string]any{"url": "https://chess-results.com/tnr123456.aspx"}); status != 200 {
		t.Fatalf("connect: %d (%v)", status, out)
	}
	if after := count(); after != before || before != 2 {
		t.Fatalf("registration categories changed: %d before, %d after", before, after)
	}
}

func TestAResultsCategoryServesItsOwnStandings(t *testing.T) {
	c, id, _ := connectedTournament(t)
	status, out, _ := c.do("GET", "/api/v1/tournaments/"+id+"/results-sections/sections/123457", nil)
	if status != 200 || num(out, "chessResultsId") != 123457 || len(out["standings"].([]any)) != 3 {
		t.Fatalf("section: %d (%v)", status, out)
	}
	// Another tournament's route cannot reach this tournament's section.
	_, other, _ := c.do("POST", "/api/v1/tournaments", map[string]any{"name": "Other"})
	if status, _, _ := c.do("GET", "/api/v1/tournaments/"+other["tournament_id"].(string)+"/results-sections/sections/123457", nil); status != 404 {
		t.Fatalf("a section of another tournament: want 404, got %d", status)
	}
}

// Refresh re-reads the category on screen; right after connecting it is
// throttled, which is the politeness floor doing its job.
func TestRefreshingACategoryIsThrottled(t *testing.T) {
	c, id, _ := connectedTournament(t)
	status, _, _ := c.do("POST", "/api/v1/tournaments/"+id+"/results-sections/sections/123456/refresh", nil)
	if status != 429 {
		t.Fatalf("refresh straight after connecting: want 429, got %d", status)
	}
}

func TestDisconnectForgetsTheResultsCategories(t *testing.T) {
	c, id, _ := connectedTournament(t)
	if status, _, _ := c.do("DELETE", "/api/v1/tournaments/"+id+"/results-sections", nil); status != 200 {
		t.Fatalf("disconnect failed")
	}
	_, out, _ := c.do("GET", "/api/v1/tournaments/"+id+"/results-sections", nil)
	if out["connected"] != false || len(out["sections"].([]any)) != 0 {
		t.Fatalf("still connected: %v", out)
	}
}

func TestConnectingIsStaffOnlyAndRefusesForeignHosts(t *testing.T) {
	c, _, _ := connectedTournament(t)
	_, tour, _ := c.do("POST", "/api/v1/tournaments", map[string]any{"name": "Event"})
	id := tour["tournament_id"].(string)
	for _, bad := range []string{"https://example.com/tnr1.aspx", "not a link"} {
		if status, _, _ := c.do("POST", "/api/v1/tournaments/"+id+"/results-sections", map[string]any{"url": bad}); status != 400 {
			t.Fatalf("%q: want 400, got %d", bad, status)
		}
	}
	student := &client{t: t, srv: c.srv}
	student.login("penny@jca.ac.th")
	if status, _, _ := student.do("POST", "/api/v1/tournaments/"+id+"/results-sections",
		map[string]any{"url": "https://chess-results.com/tnr123456.aspx"}); status != 403 {
		t.Fatalf("a student connecting: want 403, got %d", status)
	}
}

// A participant whose name is spelled differently on chess-results is linked
// by hand: staff pick the player, and the choice sticks to the registration.
func TestAParticipantCanBeLinkedToTheirChessResultsRow(t *testing.T) {
	c, id, _ := connectedTournament(t)
	status, reg, _ := c.do("POST", "/api/v1/tournament-registrations", map[string]any{
		"tournament_id": id, "participant_name": "Kitipong Srisuk",
	})
	if status != 201 {
		t.Fatalf("register: %d (%v)", status, reg)
	}
	path := "/api/v1/tournament-registrations/" + reg["tournament_registration_id"].(string)

	if status, out, _ := c.do("PATCH", path, map[string]any{
		"results_section_id": 123456, "results_player_name": "Srisuk, Kittipong",
	}); status != 200 {
		t.Fatalf("link: %d (%v)", status, out)
	}
	_, row, _ := c.do("GET", path, nil)
	if num(row, "results_section_id") != 123456 || row["results_player_name"] != "Srisuk, Kittipong" {
		t.Fatalf("link not kept: %v", row)
	}

	// Unlinking goes back to matching by name.
	c.do("PATCH", path, map[string]any{"results_section_id": nil, "results_player_name": nil})
	_, row, _ = c.do("GET", path, nil)
	if row["results_section_id"] != nil || row["results_player_name"] != nil {
		t.Fatalf("link not cleared: %v", row)
	}
}

// The public page shows what the console's Results tab shows: every connected
// chess-results category with its own table — not the registered players on
// zero points, which is what it fell back to before.
func TestPublishedResultsShowEveryChessResultsCategory(t *testing.T) {
	c, id, _ := connectedTournament(t)
	if status, out, _ := c.do("PATCH", "/api/v1/tournaments/"+id, map[string]any{"results_public": true}); status != 200 {
		t.Fatalf("publish: %d (%v)", status, out)
	}
	visitor := &client{t: t, srv: c.srv}
	status, out, _ := visitor.do("GET", "/api/v1/public/tournaments/"+id+"/results", nil)
	if status != 200 || out["source"] != "chess-results" {
		t.Fatalf("public results: %d (%v)", status, out)
	}
	// The header the page draws its banner from.
	head, _ := out["tournament"].(map[string]any)
	if head["name"] != "JCA Chessfest" || head["hasBanner"] != false {
		t.Fatalf("banner details: %v", head)
	}
	sections, _ := out["sections"].([]any)
	if len(sections) != 3 {
		t.Fatalf("want the 3 categories, got %v", out["sections"])
	}
	first := sections[0].(map[string]any)
	if first["name"] != "U12 + G12" {
		t.Fatalf("first category: %v", first["name"])
	}
	if rows, _ := first["standings"].([]any); len(rows) == 0 {
		t.Fatalf("the category's table is empty: %v", first)
	}
}
