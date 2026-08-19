package api_test

import (
	"testing"
)

/* The public registration door, exercised the way a stranger would push on it. */

// openEvent creates a tournament and opens it to the public with the given
// fee, student discount and optional limits. Returns its id and a client with
// no session at all — which is the whole point of these tests.
func openEvent(t *testing.T, fields map[string]any) (*client, string) {
	t.Helper()
	srv := newServer(t)
	staff := &client{t: t, srv: srv}
	staff.login("admin@jca.ac.th")

	body := map[string]any{
		"name": "JCA Open", "tournament_status": "Upcoming",
		"public_registration": true, "regular_fee": 500,
	}
	for k, v := range fields {
		body[k] = v
	}
	status, tour, _ := staff.do("POST", "/api/v1/tournaments", body)
	if status != 201 {
		t.Fatalf("create tournament: %d (%v)", status, tour)
	}
	// A deliberately session-less client: nothing below may depend on a token.
	return &client{t: t, srv: srv}, tour["tournament_id"].(string)
}

func entry(over map[string]any) map[string]any {
	body := map[string]any{
		"name": "Somchai Niran", "email": "somchai@example.com", "phone": "081-000-0000",
	}
	for k, v := range over {
		body[k] = v
	}
	return body
}

func TestPublicRegistrationTakesAnEntryWithoutASession(t *testing.T) {
	pub, id := openEvent(t, nil)

	status, out, _ := pub.do("POST", "/api/v1/public/tournaments/"+id+"/register", entry(nil))
	if status != 201 {
		t.Fatalf("register: want 201, got %d (%v)", status, out)
	}
	// Pending, not Approved: a stranger's submission is a request, and the desk
	// still has to say yes.
	if out["status"] != "Pending" {
		t.Fatalf("want Pending, got %v", out["status"])
	}
	if out["feeQuoted"] != float64(500) {
		t.Fatalf("want the full fee, got %v", out["feeQuoted"])
	}
}

// A tournament nobody opened must not be registrable, and must not even admit
// to existing — otherwise the endpoint is a way to enumerate tournament ids.
func TestPublicRegistrationIsClosedUntilOpened(t *testing.T) {
	pub, id := openEvent(t, map[string]any{"public_registration": false})

	status, _, _ := pub.do("POST", "/api/v1/public/tournaments/"+id+"/register", entry(nil))
	if status != 404 {
		t.Fatalf("closed event: want 404, got %d", status)
	}
	if status, _, _ := pub.do("GET", "/api/v1/public/tournaments/"+id, nil); status != 404 {
		t.Fatalf("closed event GET: want 404, got %d", status)
	}
}

func TestPublicRegistrationAppliesTheStudentDiscount(t *testing.T) {
	pub, id := openEvent(t, map[string]any{"student_discount_pct": 20})

	status, out, _ := pub.do("POST", "/api/v1/public/tournaments/"+id+"/register",
		entry(map[string]any{"isStudent": true}))
	if status != 201 {
		t.Fatalf("register: %d (%v)", status, out)
	}
	// 500 less 20% — quoted at the moment of registering, and stored, so a
	// later price change cannot rewrite what this person was told.
	if out["feeQuoted"] != float64(400) {
		t.Fatalf("want 400, got %v", out["feeQuoted"])
	}
}

// The enumeration guard, and the reason the discount is claimed rather than
// detected: an address the academy holds and one it has never seen must come
// back *identical*. If the discount only applied to real students, anyone could
// test whether a given child is a pupil here, one submission at a time.
func TestPublicRegistrationRevealsNothingAboutWhoIsAStudent(t *testing.T) {
	pub, id := openEvent(t, map[string]any{"student_discount_pct": 20})

	// penny@jca.ac.th is a seeded student account; the other address is not.
	_, known, _ := pub.do("POST", "/api/v1/public/tournaments/"+id+"/register",
		entry(map[string]any{"email": "penny@jca.ac.th", "isStudent": true}))
	_, unknown, _ := pub.do("POST", "/api/v1/public/tournaments/"+id+"/register",
		entry(map[string]any{"email": "nobody@example.com", "isStudent": true}))

	for _, k := range []string{"status", "feeQuoted", "needsApproval", "registered"} {
		if known[k] != unknown[k] {
			t.Fatalf("reply differs on %q for a known vs unknown email: %v vs %v — "+
				"this endpoint leaks who is a student", k, known[k], unknown[k])
		}
	}
}

func TestPublicRegistrationRefusesTheSameEmailTwice(t *testing.T) {
	pub, id := openEvent(t, nil)

	if status, out, _ := pub.do("POST", "/api/v1/public/tournaments/"+id+"/register", entry(nil)); status != 201 {
		t.Fatalf("first: %d (%v)", status, out)
	}
	status, _, _ := pub.do("POST", "/api/v1/public/tournaments/"+id+"/register",
		entry(map[string]any{"name": "Somebody Else"}))
	if status != 409 {
		t.Fatalf("duplicate email: want 409, got %d", status)
	}
}

func TestPublicRegistrationStopsAtCapacity(t *testing.T) {
	pub, id := openEvent(t, map[string]any{"max_participants": 1})

	if status, out, _ := pub.do("POST", "/api/v1/public/tournaments/"+id+"/register", entry(nil)); status != 201 {
		t.Fatalf("first: %d (%v)", status, out)
	}
	status, _, _ := pub.do("POST", "/api/v1/public/tournaments/"+id+"/register",
		entry(map[string]any{"email": "second@example.com"}))
	if status != 409 {
		t.Fatalf("full event: want 409, got %d", status)
	}
}

func TestPublicRegistrationStopsAfterTheDeadline(t *testing.T) {
	pub, id := openEvent(t, map[string]any{"registration_deadline": "2020-01-01"})

	status, _, _ := pub.do("POST", "/api/v1/public/tournaments/"+id+"/register", entry(nil))
	if status != 409 {
		t.Fatalf("past deadline: want 409, got %d", status)
	}
}

// A category id belonging to a different event must not attach an entry to it.
func TestPublicRegistrationRefusesAForeignCategory(t *testing.T) {
	srv := newServer(t)
	staff := &client{t: t, srv: srv}
	staff.login("admin@jca.ac.th")

	mk := func(open bool) string {
		_, tour, _ := staff.do("POST", "/api/v1/tournaments", map[string]any{
			"name": "Event", "public_registration": open, "regular_fee": 100,
		})
		return tour["tournament_id"].(string)
	}
	mine, theirs := mk(true), mk(false)
	_, cat, _ := staff.do("POST", "/api/v1/tournament-categories", map[string]any{
		"tournament_id": theirs, "name": "Under 12",
	})

	pub := &client{t: t, srv: srv}
	status, _, _ := pub.do("POST", "/api/v1/public/tournaments/"+mine+"/register",
		entry(map[string]any{"categoryId": cat["tournament_category_id"]}))
	if status != 400 {
		t.Fatalf("foreign category: want 400, got %d", status)
	}
}

func TestPublicRegistrationValidatesTheBoundary(t *testing.T) {
	pub, id := openEvent(t, nil)

	for _, tc := range []struct {
		name string
		body map[string]any
	}{
		{"no name", entry(map[string]any{"name": ""})},
		{"one-letter name", entry(map[string]any{"name": "A"})},
		{"no email", entry(map[string]any{"email": ""})},
		{"not an email", entry(map[string]any{"email": "not-an-address"})},
		{"impossible birthday", entry(map[string]any{"dateOfBirth": "31/02/2018"})},
	} {
		status, _, _ := pub.do("POST", "/api/v1/public/tournaments/"+id+"/register", tc.body)
		if status != 400 {
			t.Errorf("%s: want 400, got %d", tc.name, status)
		}
	}
}

func TestPublicTournamentListShowsOnlyOpenEvents(t *testing.T) {
	srv := newServer(t)
	staff := &client{t: t, srv: srv}
	staff.login("admin@jca.ac.th")
	for _, tc := range []struct {
		name string
		open bool
	}{{"Open Day", true}, {"Members Only", false}} {
		staff.do("POST", "/api/v1/tournaments", map[string]any{
			"name": tc.name, "public_registration": tc.open, "regular_fee": 300,
		})
	}

	pub := &client{t: t, srv: srv}
	status, out, _ := pub.do("GET", "/api/v1/public/tournaments", nil)
	if status != 200 {
		t.Fatalf("list: %d (%v)", status, out)
	}
	list, _ := out["tournaments"].([]any)
	if len(list) != 1 {
		t.Fatalf("want exactly the open event, got %d entries (%v)", len(list), list)
	}
	got := list[0].(map[string]any)
	if got["name"] != "Open Day" {
		t.Fatalf("want Open Day, got %v", got["name"])
	}
	if got["open"] != true {
		t.Fatalf("want open, got %v", got["open"])
	}
}
