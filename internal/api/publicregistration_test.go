package api_test

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"
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
		// Required of every entry now, so it belongs in the baseline rather
		// than in each case — the cases that care set it false explicitly.
		"acceptTerms": true,
	}
	for k, v := range over {
		body[k] = v
	}
	return body
}

/* A one-pixel JPEG — real magic bytes, so http.DetectContentType reads it as
   image/jpeg the same way a phone photo would, without shipping an actual
   photo into the test suite. */
var testIDPhoto = []byte{
	0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 0x4A, 0x46, 0x49, 0x46, 0x00, 0x01,
	0x01, 0x00, 0x00, 0x01, 0x00, 0x01, 0x00, 0x00, 0xFF, 0xDB, 0x00, 0x43,
	0x00, 0xFF, 0xD9,
}

// register submits a public registration the way the form does:
// multipart/form-data, after an ID card scan. With `verified`, it writes the
// ID card check a scan would have — the date of birth is the entry's
// dateOfBirth, or a child's when it gives none — and names it, as the form
// does; without, it sends no check.
func register(t *testing.T, c *client, tournamentID string, fields map[string]any, verified bool) (int, map[string]any) {
	t.Helper()
	if verified {
		dob, _ := fields["dateOfBirth"].(string)
		if dob == "" {
			dob = "2015-06-01"
		}
		fields = withCheck(t, c, tournamentID, "", dob, "idCheck", fields)
	}
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, v := range fields {
		if b, ok := v.(bool); ok {
			mw.WriteField(k, strconv.FormatBool(b))
			continue
		}
		mw.WriteField(k, fmt.Sprint(v))
	}
	mw.Close()
	req, _ := http.NewRequest(
		"POST", c.srv.URL+"/api/v1/public/tournaments/"+tournamentID+"/register", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	out := map[string]any{}
	json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

func TestPublicRegistrationTakesAnEntryWithoutASession(t *testing.T) {
	pub, id := openEvent(t, nil)

	status, out := register(t, pub, id, entry(nil), true)
	if status != 201 {
		t.Fatalf("register: want 201, got %d (%v)", status, out)
	}
	// Approved on arrival: the academy takes every entry, so there is no queue
	// left for a submission to wait in.
	if out["status"] != "Approved" {
		t.Fatalf("want Approved, got %v", out["status"])
	}
	// And the portal is told not to promise a confirmation that never comes.
	if out["needsApproval"] != false {
		t.Fatalf("want needsApproval false, got %v", out["needsApproval"])
	}
	if out["feeQuoted"] != float64(500) {
		t.Fatalf("want the full fee, got %v", out["feeQuoted"])
	}
}

// A tournament nobody opened must not be registrable, and must not even admit
// to existing — otherwise the endpoint is a way to enumerate tournament ids.
func TestPublicRegistrationIsClosedUntilOpened(t *testing.T) {
	pub, id := openEvent(t, map[string]any{"public_registration": false})

	status, _ := register(t, pub, id, entry(nil), true)
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
	status, _ := register(t, pub, id, entry(map[string]any{"isStudent": true}), true)
	if status != 400 {
		t.Fatalf("claim without an ID: want 400, got %d", status)
	}
	status, _ = register(t, pub, id,
		entry(map[string]any{"isStudent": true, "studentId": "stu_nobody"}), true)
	if status != 400 {
		t.Fatalf("claim with a made-up ID: want 400, got %d", status)
	}

	status, out := register(t, pub, id,
		entry(map[string]any{"isStudent": true, "studentId": "stu_penny"}), true)
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
	_, known := register(t, pub, id, entry(map[string]any{"email": "penny@jca.ac.th"}), true)
	_, unknown := register(t, pub, id, entry(map[string]any{"email": "nobody@example.com"}), true)

	for _, k := range []string{"status", "feeQuoted", "needsApproval", "registered"} {
		if known[k] != unknown[k] {
			t.Fatalf("reply differs on %q for a known vs unknown email: %v vs %v — "+
				"this endpoint leaks who is a student", k, known[k], unknown[k])
		}
	}
}

func TestPublicRegistrationRefusesTheSameEmailTwice(t *testing.T) {
	pub, id := openEvent(t, nil)

	if status, out := register(t, pub, id, entry(nil), true); status != 201 {
		t.Fatalf("first: %d (%v)", status, out)
	}
	status, _ := register(t, pub, id, entry(map[string]any{"name": "Somebody Else"}), true)
	if status != 409 {
		t.Fatalf("duplicate email: want 409, got %d", status)
	}
}

func TestPublicRegistrationStopsAtCapacity(t *testing.T) {
	pub, id := openEvent(t, map[string]any{"max_participants": 1})

	if status, out := register(t, pub, id, entry(nil), true); status != 201 {
		t.Fatalf("first: %d (%v)", status, out)
	}
	status, _ := register(t, pub, id, entry(map[string]any{"email": "second@example.com"}), true)
	if status != 409 {
		t.Fatalf("full event: want 409, got %d", status)
	}
}

func TestPublicRegistrationStopsAfterTheDeadline(t *testing.T) {
	pub, id := openEvent(t, map[string]any{"registration_deadline": "2020-01-01"})

	status, _ := register(t, pub, id, entry(nil), true)
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
	status, _ := register(t, pub, mine,
		entry(map[string]any{"categoryId": cat["tournament_category_id"]}), true)
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
		status, _ := register(t, pub, id, tc.body, true)
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
		status, out := register(t, pub, id, entry(nil), true)
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
		status, out := register(t, pub, id,
			entry(map[string]any{"isStudent": true, "studentId": "stu_penny"}), true)
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
	// Named apart from the package-level register() it calls, or this
	// shadows it for the rest of the test.
	registerAt := func(dob string) int {
		body := entry(map[string]any{"categoryId": catID})
		if dob != "" {
			body["dateOfBirth"] = dob
		}
		// A fresh email per attempt so the duplicate guard stays out of the way.
		body["email"] = dob + "x@example.com"
		s, _ := register(t, pub, id, body, true)
		return s
	}

	// No date of birth: the category needs one.
	if got := registerAt(""); got != 400 {
		t.Fatalf("age category without DOB: want 400, got %d", got)
	}
	// Nine on the start day — too old for under-8.
	if got := registerAt("2017-06-15"); got != 400 {
		t.Fatalf("nine-year-old in U8: want 400, got %d", got)
	}
	// Seven on the start day — allowed.
	if got := registerAt("2019-06-15"); got != 201 {
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
	// …and so do the academy's own families, whose portal links it, and
	// whose tournament row says there is one to open.
	parent := &client{t: t, srv: pub.srv}
	parent.login("sandy01234@gmail.com")
	if status, _, _ := parent.do("GET", "/api/v1/tournaments/"+id+"/regulation", nil); status != 200 {
		t.Fatalf("private regulation, signed-in parent: want 200, got %d", status)
	}
	// 1, as SQLite answers EXISTS — read as true, the same as has_banner.
	if _, row, _ := parent.do("GET", "/api/v1/tournaments/"+id, nil); row["has_regulation"] != float64(1) && row["has_regulation"] != true {
		t.Fatalf("has_regulation on the parent's tournament row: %v", row["has_regulation"])
	}
	// A draft stays the organiser's, signed in or not.
	staff.do("PATCH", "/api/v1/tournaments/"+id, map[string]any{"draft": true})
	if status, _, _ := parent.do("GET", "/api/v1/tournaments/"+id+"/regulation", nil); status != 404 {
		t.Fatalf("draft regulation, parent: want 404, got %d", status)
	}
}

// A venue's map link is stored once, at tournament creation (see 0037), and
// read back exactly as given rather than recomputed — so a stranger reading
// the public listing sees the same link the desk set.
func TestPublicTournamentCarriesTheVenueMapLink(t *testing.T) {
	mapURL := "https://www.google.com/maps/search/?api=1&query=Wellington+College+Bangkok"
	pub, id := openEvent(t, map[string]any{"venue_map_url": mapURL})

	status, out, _ := pub.do("GET", "/api/v1/public/tournaments/"+id, nil)
	if status != 200 {
		t.Fatalf("get: %d (%v)", status, out)
	}
	tour, _ := out["tournament"].(map[string]any)
	if tour["venueMapUrl"] != mapURL {
		t.Fatalf("want venueMapUrl %q, got %v", mapURL, tour["venueMapUrl"])
	}
}

// The ID card step is required: an entry without an ID card check is
// refused, whatever date of birth it typed.
func TestPublicRegistrationRequiresTheIDCardCheck(t *testing.T) {
	pub, id := openEvent(t, nil)

	status, out := register(t, pub, id, entry(map[string]any{"dateOfBirth": "2015-01-01"}), false)
	if status != 400 {
		t.Fatalf("no ID card check: want 400, got %d (%v)", status, out)
	}
}

// A check is spent by the entry that uses it: it cannot be kept and named
// again for somebody else.
func TestAnIDCardCheckIsUsedOnce(t *testing.T) {
	pub, id := openEvent(t, nil)
	body := withCheck(t, pub, id, "", "2015-01-01", "idCheck", entry(nil))
	if status, out := register(t, pub, id, body, false); status != 201 {
		t.Fatalf("first entry: %d (%v)", status, out)
	}
	body["email"] = "someone.else@example.com"
	if status, _ := register(t, pub, id, body, false); status != 400 {
		t.Fatalf("reused check: want 400, got %d", status)
	}
}

// The photo itself is not kept anywhere: there is no table for it and no
// endpoint that serves one.
func TestAnIDCardPhotoIsNotKept(t *testing.T) {
	pub, id := openEvent(t, nil)
	status, reg := register(t, pub, id, entry(nil), true)
	if status != 201 {
		t.Fatalf("register: want 201, got %d (%v)", status, reg)
	}
	v, _ := serverDBs.Load(pub.srv.URL)
	var n int
	v.(*sql.DB).QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name = 'tournament_registration_document'`).Scan(&n)
	if n != 0 {
		t.Fatal("the ID photo table still exists")
	}
	staff := &client{t: t, srv: pub.srv}
	staff.login("admin@jca.ac.th")
	regID := firstRegistrationID(t, staff, id)
	if status, _, _ := staff.do("GET", "/api/v1/tournaments/registrations/"+regID+"/document", nil); status == 200 {
		t.Fatal("an ID photo is still served")
	}
}

/* The terms, the nickname and the age — what the academy's entry form asks
 * that the schema had nowhere to put. Registered through `register`, not a
 * raw JSON post: every entry needs the ID photo since 0036, and a bare
 * `pub.do` would now fail multipart parsing before any of these rules ran. */

// The five numbered conditions on the form are the point of recording this:
// "did this person agree not to be refunded" is asked three weeks later, and a
// record that can mean "we defaulted it to yes" answers nothing.
func TestAnEntryIsRefusedWithoutAcceptingTheTerms(t *testing.T) {
	pub, id := openEvent(t, nil)
	for _, v := range []any{false, nil} {
		body := entry(nil)
		if v == nil {
			delete(body, "acceptTerms")
		} else {
			body["acceptTerms"] = v
		}
		status, out := register(t, pub, id, body, true)
		if status != 400 {
			t.Errorf("acceptTerms %v: want 400, got %d (%v)", v, status, out)
		}
	}
}

// Stamped by the server. A client-supplied timestamp on a consent record is
// worth nothing, so reaching the insert is what "accepted" means.
func TestAcceptingTheTermsIsRecordedWithATime(t *testing.T) {
	pub, id := openEvent(t, nil)
	if status, out := register(t, pub, id, entry(map[string]any{"nickname": "Chai", "age": 11}), true); status != 201 {
		t.Fatalf("register: %d (%v)", status, out)
	}

	staff := &client{t: t, srv: pub.srv}
	staff.login("admin@jca.ac.th")
	_, _, rows := staff.do("GET", "/api/v1/tournament-registrations?tournament_id="+id, nil)
	if len(rows) == 0 {
		t.Fatalf("no registration rows")
	}
	row := rows[0]
	if row["terms_accepted_at"] == nil || row["terms_accepted_at"] == "" {
		t.Errorf("terms_accepted_at should be stamped, got %v", row["terms_accepted_at"])
	}
	if row["nickname"] != "Chai" {
		t.Errorf("nickname = %v, want Chai", row["nickname"])
	}
	if row["participant_age"] != float64(11) {
		t.Errorf("participant_age = %v, want 11", row["participant_age"])
	}
}

// The age group is decided by the ID card's date of birth, not by what the
// form typed: by birth year, somebody born in the event's year minus 12 may
// play U12; a year earlier is too old.
func TestTheIDCardDecidesTheAgeGroup(t *testing.T) {
	pub, id := openEvent(t, map[string]any{"start_date": "2026-11-01"})
	staff := &client{t: t, srv: pub.srv}
	staff.login("admin@jca.ac.th")
	u12 := categoryOf(t, staff, id, "Under 12")

	for _, tc := range []struct {
		card string
		want int
	}{{"2015-12-31", 201}, {"2014-01-01", 201}, {"2013-12-31", 400}} {
		body := withCheck(t, pub, id, "", tc.card, "idCheck", entry(map[string]any{
			"categoryId": u12, "dateOfBirth": "2020-01-01", // typed: ignored
			"email":      "born" + tc.card + "@example.com",
		}))
		status, out := register(t, pub, id, body, false)
		if status != tc.want {
			t.Errorf("card %s: want %d, got %d (%v)", tc.card, tc.want, status, out)
		}
	}
}

// And the date of birth still wins where there is one: it is what an ID card
// proves, where an age is what somebody typed.
func TestADateOfBirthOutranksAClaimedAge(t *testing.T) {
	pub, id := openEvent(t, nil)
	staff := &client{t: t, srv: pub.srv}
	staff.login("admin@jca.ac.th")
	u12 := categoryOf(t, staff, id, "U12 Junior")

	// Claims 10, but the card says they were born twenty years ago.
	born := time.Now().AddDate(-20, 0, 0).Format("2006-01-02")
	status, _ := register(t, pub, id, entry(map[string]any{"categoryId": u12, "age": 10, "dateOfBirth": born}), true)
	if status != 400 {
		t.Fatalf("want 400 on the date of birth, got %d", status)
	}
}

// A group with no age in its name takes anybody — OPEN is a real category.
func TestAnOpenGroupHasNoAgeRule(t *testing.T) {
	pub, id := openEvent(t, nil)
	staff := &client{t: t, srv: pub.srv}
	staff.login("admin@jca.ac.th")
	open := categoryOf(t, staff, id, "OPEN (FIDE Rated Event)")

	status, out := register(t, pub, id, entry(map[string]any{"categoryId": open, "age": 47}), true)
	if status != 201 {
		t.Fatalf("OPEN should take a 47-year-old: %d (%v)", status, out)
	}
}

func TestAnImplausibleAgeIsRefused(t *testing.T) {
	pub, id := openEvent(t, nil)
	for _, age := range []int{-1, 121} {
		status, _ := register(t, pub, id, entry(map[string]any{"age": age}), true)
		if status != 400 {
			t.Errorf("age %d: want 400, got %d", age, status)
		}
	}
}

// withCheck writes an ID card check for the tournament (and child, when one
// is named) and returns fields with its id added under key — "idCheck" on
// the public form, "id_check" from the parent portal.
func withCheck(t *testing.T, c *client, tournamentID, studentID, dob, key string, fields map[string]any) map[string]any {
	t.Helper()
	v, ok := serverDBs.Load(c.srv.URL)
	if !ok {
		t.Fatal("register: server has no database on record (use newServerOn)")
	}
	d := v.(*sql.DB)
	id := fmt.Sprintf("idc_test_%d", time.Now().UnixNano())
	// A child who does not exist cannot be named (the foreign key); the
	// entry is refused for that before the check is looked at.
	var student any
	var n int
	d.QueryRow(`SELECT COUNT(*) FROM student WHERE student_id = ?`, studentID).Scan(&n)
	if n > 0 {
		student = studentID
	}
	if _, err := d.Exec(`INSERT INTO id_card_check (id_card_check_id, tournament_id, student_id, date_of_birth, name)
	                     VALUES (?,?,?,?, 'Read Off Card')`, id, tournamentID, student, dob); err != nil {
		t.Fatal(err)
	}
	out := map[string]any{}
	for k, v := range fields {
		out[k] = v
	}
	out[key] = id
	return out
}
