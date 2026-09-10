package api_test

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"
	"time"
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

	// The claim alone is no longer enough — the discount needs an ID the
	// academy can find.
	status, _, _ := pub.do("POST", "/api/v1/public/tournaments/"+id+"/register",
		entry(map[string]any{"isStudent": true}))
	if status != 400 {
		t.Fatalf("claim without an ID: want 400, got %d", status)
	}
	status, _, _ = pub.do("POST", "/api/v1/public/tournaments/"+id+"/register",
		entry(map[string]any{"isStudent": true, "studentId": "stu_nobody"}))
	if status != 400 {
		t.Fatalf("claim with a made-up ID: want 400, got %d", status)
	}

	status, out, _ := pub.do("POST", "/api/v1/public/tournaments/"+id+"/register",
		entry(map[string]any{"isStudent": true, "studentId": "stu_penny"}))
	if status != 201 {
		t.Fatalf("register: %d (%v)", status, out)
	}
	// 500 less 20% — quoted at the moment of registering, and stored, so a
	// later price change cannot rewrite what this person was told.
	if out["feeQuoted"] != float64(400) {
		t.Fatalf("want 400, got %v", out["feeQuoted"])
	}
}

// The enumeration guard, narrowed since the ID check arrived: the *email*
// path still reveals nothing — an address the academy holds and one it has
// never seen come back identical. The student-ID check does disclose whether
// an opaque ID exists, which the product asked for; it is behind the tightest
// rate limit on the API (10/min) and IDs carry no name.
func TestPublicRegistrationRevealsNothingAboutWhoIsAStudent(t *testing.T) {
	pub, id := openEvent(t, map[string]any{"student_discount_pct": 20})

	// penny@jca.ac.th is a seeded student account; the other address is not.
	_, known, _ := pub.do("POST", "/api/v1/public/tournaments/"+id+"/register",
		entry(map[string]any{"email": "penny@jca.ac.th"}))
	_, unknown, _ := pub.do("POST", "/api/v1/public/tournaments/"+id+"/register",
		entry(map[string]any{"email": "nobody@example.com"}))

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

func TestEarlyBirdWindow(t *testing.T) {
	future := time.Now().AddDate(0, 0, 7).Format("2006-01-02")
	past := time.Now().AddDate(0, 0, -1).Format("2006-01-02")

	t.Run("open window quotes the early price to an outsider", func(t *testing.T) {
		pub, id := openEvent(t, map[string]any{
			"early_bird_fee": 300, "early_bird_deadline": future, "student_discount_pct": 20,
		})
		_, page, _ := pub.do("GET", "/api/v1/public/tournaments/"+id, nil)
		tour, _ := page["tournament"].(map[string]any)
		if tour["earlyBirdActive"] != true || tour["fee"] != float64(300) {
			t.Fatalf("window open: %v / %v", tour["earlyBirdActive"], tour["fee"])
		}
		status, out, _ := pub.do("POST", "/api/v1/public/tournaments/"+id+"/register", entry(nil))
		if status != 201 || out["feeQuoted"] != float64(300) {
			t.Fatalf("outsider in the window: %d, fee %v", status, out["feeQuoted"])
		}
	})

	t.Run("a student is priced off the regular fee, not the early one", func(t *testing.T) {
		// Early bird is for outside participants; the student discount is for
		// students. 500 less 20% is 400 even while early bird says 300.
		pub, id := openEvent(t, map[string]any{
			"early_bird_fee": 300, "early_bird_deadline": future, "student_discount_pct": 20,
		})
		status, out, _ := pub.do("POST", "/api/v1/public/tournaments/"+id+"/register",
			entry(map[string]any{"isStudent": true, "studentId": "stu_penny"}))
		if status != 201 || out["feeQuoted"] != float64(400) {
			t.Fatalf("student in the window: %d, fee %v", status, out["feeQuoted"])
		}
	})

	t.Run("a closed window quotes the regular price", func(t *testing.T) {
		pub, id := openEvent(t, map[string]any{
			"early_bird_fee": 300, "early_bird_deadline": past,
		})
		_, page, _ := pub.do("GET", "/api/v1/public/tournaments/"+id, nil)
		tour, _ := page["tournament"].(map[string]any)
		if tour["earlyBirdActive"] != false || tour["fee"] != float64(500) {
			t.Fatalf("window closed: %v / %v", tour["earlyBirdActive"], tour["fee"])
		}
	})
}

func TestCategoryAgeRule(t *testing.T) {
	pub, id := openEvent(t, map[string]any{"start_date": "2026-12-01"})
	// The admin adds a U8 category; the age rule lives in that name.
	staff := &client{t: t, srv: pub.srv}
	staff.login("admin@jca.ac.th")
	status, cat, _ := staff.do("POST", "/api/v1/tournament-categories", map[string]any{
		"tournament_id": id, "name": "U8 Boys",
	})
	if status != 201 {
		t.Fatalf("create category: %d (%v)", status, cat)
	}
	catID := cat["tournament_category_id"].(string)
	register := func(dob string) int {
		body := entry(map[string]any{"categoryId": catID})
		if dob != "" {
			body["dateOfBirth"] = dob
		}
		// A fresh email per attempt so the duplicate guard stays out of the way.
		body["email"] = dob + "x@example.com"
		s, _, _ := pub.do("POST", "/api/v1/public/tournaments/"+id+"/register", body)
		return s
	}

	// No date of birth: the category needs one.
	if got := register(""); got != 400 {
		t.Fatalf("age category without DOB: want 400, got %d", got)
	}
	// Nine on the start day — too old for under-8.
	if got := register("2017-06-15"); got != 400 {
		t.Fatalf("nine-year-old in U8: want 400, got %d", got)
	}
	// Seven on the start day — allowed.
	if got := register("2019-06-15"); got != 201 {
		t.Fatalf("seven-year-old in U8: want 201, got %d", got)
	}
}

func TestRegulationLifecycle(t *testing.T) {
	pub, id := openEvent(t, nil)
	staff := &client{t: t, srv: pub.srv}
	staff.login("admin@jca.ac.th")

	upload := func(c *client, body []byte, filename string) int {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		fw, _ := mw.CreateFormFile("file", filename)
		fw.Write(body)
		mw.Close()
		req, _ := http.NewRequest("POST", pub.srv.URL+"/api/v1/tournaments/"+id+"/regulation", &buf)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		if c.token != "" {
			req.Header.Set("Authorization", "Bearer "+c.token)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	pdf := append([]byte("%PDF-1.4\n"), bytes.Repeat([]byte("a"), 200)...)

	// A stranger may not attach files to the academy's tournaments.
	if got := upload(pub, pdf, "reg.pdf"); got != 401 && got != 403 {
		t.Fatalf("anonymous upload: want 401/403, got %d", got)
	}
	// Not-a-document is refused however it is named.
	if got := upload(staff, []byte("#!/bin/sh\necho hi"), "reg.pdf"); got != http.StatusUnsupportedMediaType {
		t.Fatalf("script as regulation: want 415, got %d", got)
	}
	if got := upload(staff, pdf, `weird"; name.pdf`); got != 200 {
		t.Fatalf("upload: want 200, got %d", got)
	}

	// The page now says a regulation exists…
	_, page, _ := pub.do("GET", "/api/v1/public/tournaments/"+id, nil)
	tour, _ := page["tournament"].(map[string]any)
	if tour["hasRegulation"] != true {
		t.Fatalf("hasRegulation not set: %v", tour["hasRegulation"])
	}
	// …and anyone can read it, with the filename made safe for the header.
	res, err := http.Get(pub.srv.URL + "/api/v1/tournaments/" + id + "/regulation")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || !bytes.Equal(got, pdf) {
		t.Fatalf("public read: %d, %d bytes", res.StatusCode, len(got))
	}
	if cd := res.Header.Get("Content-Disposition"); strings.Contains(cd, `"; `) || !strings.Contains(cd, "inline") {
		t.Fatalf("unsafe content-disposition: %q", cd)
	}
	if ct := res.Header.Get("Content-Type"); ct != "application/pdf" {
		t.Fatalf("content-type: %q", ct)
	}

	// A private tournament's regulation is not readable anonymously…
	staff.do("PATCH", "/api/v1/tournaments/"+id, map[string]any{"public_registration": false})
	if res, _ := http.Get(pub.srv.URL + "/api/v1/tournaments/" + id + "/regulation"); res.StatusCode != 404 {
		t.Fatalf("private regulation, anonymous: want 404, got %d", res.StatusCode)
	}
	// …but staff still see it.
	if status, _, _ := staff.do("GET", "/api/v1/tournaments/"+id+"/regulation", nil); status != 200 {
		t.Fatalf("private regulation, staff: want 200, got %d", status)
	}
}
