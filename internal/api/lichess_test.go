package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

/* A stand-in for lichess.org, shaped from the real responses:
   perfs keyed by game type, each with rating/games/prov, and a public
   profile.bio — which is what account verification hangs on. */

type lichessStub struct {
	mu        sync.Mutex
	srv       *httptest.Server
	bios      map[string]string
	rating    map[string]int
	calls     int
	bulkCalls int
}

func newLichessStub(t *testing.T) *lichessStub {
	s := &lichessStub{
		bios:   map[string]string{"pennyplays": ""},
		rating: map[string]int{"pennyplays": 1240},
	}
	body := func(name string) map[string]any {
		s.mu.Lock()
		defer s.mu.Unlock()
		return map[string]any{
			"id": strings.ToLower(name), "username": name,
			"profile": map[string]any{"bio": s.bios[strings.ToLower(name)]},
			"perfs": map[string]any{
				"blitz":     map[string]any{"rating": s.rating[strings.ToLower(name)], "games": 210},
				"rapid":     map[string]any{"rating": s.rating[strings.ToLower(name)] + 60, "games": 88},
				"puzzle":    map[string]any{"rating": 1610, "games": 940},
				"classical": map[string]any{"rating": 1500, "games": 2, "prov": true},
				// Never played: must not appear as a rating of any kind.
				"bullet": map[string]any{"rating": 1500, "games": 0, "prov": true},
			},
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/user/{name}", func(w http.ResponseWriter, r *http.Request) {
		name := strings.ToLower(r.PathValue("name"))
		s.mu.Lock()
		_, known := s.bios[name]
		s.calls++
		s.mu.Unlock()
		if !known {
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error":"Not found"}`))
			return
		}
		json.NewEncoder(w).Encode(body(name))
	})
	mux.HandleFunc("POST /api/users", func(w http.ResponseWriter, r *http.Request) {
		raw := make([]byte, 4096)
		n, _ := r.Body.Read(raw)
		s.mu.Lock()
		s.bulkCalls++
		s.mu.Unlock()
		out := []map[string]any{}
		for _, name := range strings.Split(string(raw[:n]), ",") {
			name = strings.ToLower(strings.TrimSpace(name))
			s.mu.Lock()
			_, known := s.bios[name]
			s.mu.Unlock()
			if known {
				out = append(out, body(name))
			}
		}
		json.NewEncoder(w).Encode(out)
	})

	s.srv = httptest.NewServer(mux)
	t.Cleanup(s.srv.Close)
	return s
}

func (s *lichessStub) setBio(name, bio string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bios[strings.ToLower(name)] = bio
}

func (s *lichessStub) setRating(name string, r int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rating[strings.ToLower(name)] = r
}

func (s *lichessStub) counts() (single, bulk int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls, s.bulkCalls
}

func newLichessServer(t *testing.T) (*client, *lichessStub) {
	t.Helper()
	stub := newLichessStub(t)
	t.Setenv("LICHESS_API_BASE", stub.srv.URL)
	return &client{t: t, srv: newServer(t)}, stub
}

func asStudent(t *testing.T, srv *client, email string) *client {
	c := &client{t: t, srv: srv.srv}
	c.login(email)
	return c
}

/* ---- linking ---- */

func TestLichessLinkStoresRatingsImmediately(t *testing.T) {
	base, _ := newLichessServer(t)
	penny := asStudent(t, base, "penny@jca.ac.th")

	status, obj, _ := penny.do("POST", "/api/v1/lichess/link", map[string]string{"username": "PennyPlays"})
	if status != 201 {
		t.Fatalf("link: status %d (%v)", status, obj)
	}
	if obj["verified"] != false {
		t.Errorf("a fresh link must not be verified: %v", obj)
	}
	if code, _ := obj["verifyCode"].(string); !strings.HasPrefix(code, "JTRAX-") {
		t.Errorf("expected a verification code, got %v", obj["verifyCode"])
	}

	_, mine, _ := penny.do("GET", "/api/v1/lichess/me", nil)
	link, _ := mine["link"].(map[string]any)
	ratings, _ := link["ratings"].([]any)
	if len(ratings) == 0 {
		t.Fatal("ratings should be stored on link, so the screen is not empty while the pupil edits their bio")
	}
	got := map[string]float64{}
	for _, r := range ratings {
		m := r.(map[string]any)
		got[m["perf"].(string)] = m["rating"].(float64)
	}
	if got["blitz"] != 1240 || got["rapid"] != 1300 {
		t.Errorf("ratings = %v", got)
	}
	// A game type never played must not appear at all — recording it as 1500
	// would seed a leaderboard with a rating the pupil has not earned.
	if _, ok := got["bullet"]; ok {
		t.Errorf("bullet has 0 games and should be absent, got %v", got)
	}
}

func TestLichessLinkRejectsUnknownAndMalformedNames(t *testing.T) {
	base, _ := newLichessServer(t)
	penny := asStudent(t, base, "penny@jca.ac.th")

	if status, _, _ := penny.do("POST", "/api/v1/lichess/link",
		map[string]string{"username": "nobody-here-at-all"}); status != 404 {
		t.Errorf("unknown account: status %d, want 404", status)
	}
	for _, bad := range []string{"", "a", "has space", "toolong" + strings.Repeat("x", 40), "../admin"} {
		if status, _, _ := penny.do("POST", "/api/v1/lichess/link",
			map[string]string{"username": bad}); status != 400 && status != 404 {
			t.Errorf("username %q: status %d, want 400 or 404", bad, status)
		}
	}
}

/* ---- verification: the whole point ---- */

func TestLichessVerificationNeedsTheCodeInTheBio(t *testing.T) {
	base, stub := newLichessServer(t)
	penny := asStudent(t, base, "penny@jca.ac.th")
	_, link, _ := penny.do("POST", "/api/v1/lichess/link", map[string]string{"username": "PennyPlays"})
	code := link["verifyCode"].(string)

	// Bio is empty: the claim is unproven and must stay that way.
	_, out, _ := penny.do("POST", "/api/v1/lichess/verify", nil)
	if out["verified"] != false {
		t.Fatalf("verified with an empty bio: %v", out)
	}

	// A different code is not good enough either.
	stub.setBio("pennyplays", "JTRAX-AAAAAAAA")
	_, out, _ = penny.do("POST", "/api/v1/lichess/verify", nil)
	if out["verified"] != false {
		t.Fatalf("verified with someone else's code: %v", out)
	}

	// The real code, with the tidy text a student would actually write.
	stub.setBio("pennyplays", "JCA Chess Academy — "+code+" — 8 years old")
	_, out, _ = penny.do("POST", "/api/v1/lichess/verify", nil)
	if out["verified"] != true {
		t.Fatalf("did not verify with the code present: %v", out)
	}

	// The code is spent: it must not be handed out again.
	_, mine, _ := penny.do("GET", "/api/v1/lichess/me", nil)
	linkNow, _ := mine["link"].(map[string]any)
	if linkNow["verified"] != true {
		t.Errorf("link should be verified: %v", linkNow)
	}
	if _, present := linkNow["verifyCode"]; present {
		t.Errorf("a spent verification code was returned again: %v", linkNow)
	}
}

/* ---- who may see and change what ---- */

func TestLichessStudentsCannotLinkForEachOther(t *testing.T) {
	base, _ := newLichessServer(t)
	penny := asStudent(t, base, "penny@jca.ac.th")
	uri := asStudent(t, base, "uri@jca.ac.th")

	// Uri claims the account under Penny's student id.
	status, _, _ := uri.do("POST", "/api/v1/lichess/link",
		map[string]string{"username": "PennyPlays", "studentId": "stu_penny"})
	if status != 403 {
		t.Fatalf("a student linked an account for another student: status %d", status)
	}
	_ = penny
}

func TestLichessOneAccountCannotBeClaimedTwice(t *testing.T) {
	base, _ := newLichessServer(t)
	penny := asStudent(t, base, "penny@jca.ac.th")
	uri := asStudent(t, base, "uri@jca.ac.th")

	if status, _, _ := penny.do("POST", "/api/v1/lichess/link",
		map[string]string{"username": "PennyPlays"}); status != 201 {
		t.Fatalf("first link failed: %d", status)
	}
	status, obj, _ := uri.do("POST", "/api/v1/lichess/link", map[string]string{"username": "PennyPlays"})
	if status != 409 {
		t.Fatalf("the same account was linked to two students: status %d (%v)", status, obj)
	}
}

func TestLichessStudentSeesOnlyTheirOwnLink(t *testing.T) {
	base, _ := newLichessServer(t)
	penny := asStudent(t, base, "penny@jca.ac.th")
	uri := asStudent(t, base, "uri@jca.ac.th")
	penny.do("POST", "/api/v1/lichess/link", map[string]string{"username": "PennyPlays"})

	_, _, list := uri.do("GET", "/api/v1/lichess/links", nil)
	for _, l := range list {
		if l["studentId"] != "stu_uri" {
			t.Errorf("a student saw another student's link: %v", l)
		}
	}
	// And cannot read their history either.
	if status, _, _ := uri.do("GET", "/api/v1/lichess/history/stu_penny", nil); status != 404 {
		t.Errorf("history of another student: status %d, want 404", status)
	}
}

func TestLichessStaffSeeEveryLinkAndCanUnlink(t *testing.T) {
	base, _ := newLichessServer(t)
	penny := asStudent(t, base, "penny@jca.ac.th")
	penny.do("POST", "/api/v1/lichess/link", map[string]string{"username": "PennyPlays"})

	admin := &client{t: t, srv: base.srv}
	admin.login("admin@jca.ac.th")
	_, _, list := admin.do("GET", "/api/v1/lichess/links", nil)
	if len(list) != 1 {
		t.Fatalf("staff should see the link, got %d", len(list))
	}
	if list[0]["studentName"] != "Penny" {
		t.Errorf("expected the student's name alongside, got %v", list[0])
	}

	if status, _, _ := admin.do("DELETE", "/api/v1/lichess/link?studentId=stu_penny", nil); status != 200 {
		t.Fatalf("staff unlink failed: %d", status)
	}
	_, _, list = admin.do("GET", "/api/v1/lichess/links", nil)
	if len(list) != 0 {
		t.Errorf("link survived unlinking: %v", list)
	}
}

func TestLichessStaffLinkIsMarkedUnverified(t *testing.T) {
	base, _ := newLichessServer(t)
	admin := &client{t: t, srv: base.srv}
	admin.login("admin@jca.ac.th")

	status, obj, _ := admin.do("POST", "/api/v1/lichess/link",
		map[string]string{"username": "PennyPlays", "studentId": "stu_penny"})
	if status != 201 {
		t.Fatalf("staff link: %d (%v)", status, obj)
	}
	// Staff cannot edit a pupil's Lichess bio, so handing them the code would
	// only invite passing it around.
	if _, present := obj["verifyCode"]; present {
		t.Errorf("the verification code was handed to staff: %v", obj)
	}
	_, _, list := admin.do("GET", "/api/v1/lichess/links", nil)
	if list[0]["verified"] != false || list[0]["addedByStaff"] != true {
		t.Errorf("a staff-entered link must be marked unverified and staff-added, got %v", list[0])
	}
}

func TestLichessSyncIsStaffOnly(t *testing.T) {
	base, _ := newLichessServer(t)
	penny := asStudent(t, base, "penny@jca.ac.th")
	if status, _, _ := penny.do("POST", "/api/v1/lichess/sync", nil); status != 403 {
		t.Errorf("a student forced a sync: status %d, want 403", status)
	}
}

/* ---- syncing ---- */

func TestLichessSyncUsesOneBulkCallAndUpdatesRatings(t *testing.T) {
	base, stub := newLichessServer(t)
	penny := asStudent(t, base, "penny@jca.ac.th")
	uri := asStudent(t, base, "uri@jca.ac.th")
	stub.setBio("uriplays", "")
	stub.setRating("uriplays", 900)
	penny.do("POST", "/api/v1/lichess/link", map[string]string{"username": "PennyPlays"})
	uri.do("POST", "/api/v1/lichess/link", map[string]string{"username": "UriPlays"})

	stub.setRating("pennyplays", 1305)
	admin := &client{t: t, srv: base.srv}
	admin.login("admin@jca.ac.th")

	_, before := stub.counts()
	status, obj, _ := admin.do("POST", "/api/v1/lichess/sync", nil)
	if status != 200 {
		t.Fatalf("sync: %d (%v)", status, obj)
	}
	if obj["synced"].(float64) != 2 {
		t.Errorf("synced = %v, want 2", obj["synced"])
	}
	_, after := stub.counts()
	// Two students, one request. Per-student polling is what gets an
	// integration rate-limited.
	if after-before != 1 {
		t.Errorf("sync made %d bulk calls for 2 students, want 1", after-before)
	}

	_, _, list := admin.do("GET", "/api/v1/lichess/links", nil)
	for _, l := range list {
		if l["studentId"] != "stu_penny" {
			continue
		}
		for _, r := range l["ratings"].([]any) {
			m := r.(map[string]any)
			if m["perf"] == "blitz" && m["rating"].(float64) != 1305 {
				t.Errorf("blitz not refreshed: %v", m)
			}
		}
	}
}

func TestLichessProvisionalRatingsAreFlagged(t *testing.T) {
	base, _ := newLichessServer(t)
	penny := asStudent(t, base, "penny@jca.ac.th")
	penny.do("POST", "/api/v1/lichess/link", map[string]string{"username": "PennyPlays"})

	_, mine, _ := penny.do("GET", "/api/v1/lichess/me", nil)
	link := mine["link"].(map[string]any)
	found := false
	for _, r := range link["ratings"].([]any) {
		m := r.(map[string]any)
		if m["perf"] == "classical" {
			found = true
			if m["provisional"] != true {
				t.Errorf("a 2-game classical rating must be flagged provisional: %v", m)
			}
		}
	}
	if !found {
		t.Fatal("classical rating missing")
	}
}

func TestLichessHistoryRecordsADayPerSync(t *testing.T) {
	base, stub := newLichessServer(t)
	penny := asStudent(t, base, "penny@jca.ac.th")
	penny.do("POST", "/api/v1/lichess/link", map[string]string{"username": "PennyPlays"})

	admin := &client{t: t, srv: base.srv}
	admin.login("admin@jca.ac.th")
	stub.setRating("pennyplays", 1400)
	admin.do("POST", "/api/v1/lichess/sync", nil)
	stub.setRating("pennyplays", 1450)
	admin.do("POST", "/api/v1/lichess/sync", nil)

	_, out, _ := penny.do("GET", "/api/v1/lichess/history/stu_penny?perf=blitz", nil)
	points, _ := out["points"].([]any)
	// Three syncs in one afternoon is one day of history, and the first
	// reading is the one kept — so a chart does not rewrite itself.
	if len(points) != 1 {
		t.Fatalf("want 1 point for one day, got %d (%v)", len(points), points)
	}
	if p := points[0].(map[string]any); p["rating"].(float64) != 1240 {
		t.Errorf("the first reading of the day should be kept, got %v", p)
	}
}

func TestLichessUnknownStudentHasNoLink(t *testing.T) {
	base, _ := newLichessServer(t)
	penny := asStudent(t, base, "penny@jca.ac.th")
	_, out, _ := penny.do("GET", "/api/v1/lichess/me", nil)
	if out["linked"] != false {
		t.Errorf("want linked:false before linking, got %v", out)
	}
}
