package api_test

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"testing"
)

/* A tournament is reviewed before it goes live: the wizard saves a draft,
 * the organiser looks at the real page through a preview link, and Publish
 * takes it live. */

// draftEvent saves a draft the way the wizard does, with one category.
func draftEvent(t *testing.T) (staff *client, pub *client, id string) {
	t.Helper()
	srv := newServer(t)
	staff = &client{t: t, srv: srv}
	staff.login("admin@jca.ac.th")
	status, tour, _ := staff.do("POST", "/api/v1/tournaments", map[string]any{
		"name": "JCA Rapid", "tournament_status": "Upcoming", "draft": true, "regular_fee": 400,
	})
	if status != 201 {
		t.Fatalf("create draft: %d (%v)", status, tour)
	}
	id = tour["tournament_id"].(string)
	if s, out, _ := staff.do("POST", "/api/v1/tournament-categories",
		map[string]any{"tournament_id": id, "name": "U10"}); s != 201 {
		t.Fatalf("category: %d (%v)", s, out)
	}
	return staff, &client{t: t, srv: srv}, id
}

func previewToken(t *testing.T, staff *client, id string) string {
	t.Helper()
	status, out, _ := staff.do("POST", "/api/v1/tournaments/"+id+"/preview", nil)
	if status != 200 {
		t.Fatalf("preview link: %d (%v)", status, out)
	}
	return out["token"].(string)
}

func TestADraftIsNotPublic(t *testing.T) {
	staff, pub, id := draftEvent(t)

	// Not on the public page, even with registration switched on by hand…
	staff.do("PATCH", "/api/v1/tournaments/"+id, map[string]any{"public_registration": true})
	if s, _, _ := pub.do("GET", "/api/v1/public/tournaments/"+id, nil); s != 404 {
		t.Fatalf("public page of a draft: want 404, got %d", s)
	}
	if s, _, _ := pub.do("POST", "/api/v1/public/tournaments/"+id+"/register", entry(nil)); s != 404 {
		t.Fatalf("registering on a draft: want 404, got %d", s)
	}
	// …and not in a parent's list.
	parent := &client{t: t, srv: pub.srv}
	parent.login("sandy01234@gmail.com")
	_, _, rows := parent.do("GET", "/api/v1/tournaments", nil)
	for _, r := range rows {
		if r["tournament_id"] == id {
			t.Fatalf("a parent can list the draft")
		}
	}
}

func TestADraftIsPreviewedThroughItsLink(t *testing.T) {
	staff, pub, id := draftEvent(t)
	token := previewToken(t, staff, id)

	// The same link every time, so a page already open keeps working.
	if again := previewToken(t, staff, id); again != token {
		t.Fatalf("preview token changed: %q then %q", token, again)
	}
	// Staff only.
	if s, _, _ := pub.do("POST", "/api/v1/tournaments/"+id+"/preview", nil); s != 401 && s != 403 {
		t.Fatalf("anonymous preview link: want 401/403, got %d", s)
	}

	s, page, _ := pub.do("GET", "/api/v1/public/tournaments/"+id+"/preview?preview="+token, nil)
	if s != 200 || page["preview"] != true {
		t.Fatalf("preview: %d (%v)", s, page)
	}
	tour, _ := page["tournament"].(map[string]any)
	cats, _ := page["categories"].([]any)
	if tour["name"] != "JCA Rapid" || len(cats) != 1 {
		t.Fatalf("preview shows the wrong event: %v %v", tour, cats)
	}
	if s, _, _ := pub.do("GET", "/api/v1/public/tournaments/"+id+"/preview?preview=nope", nil); s != 404 {
		t.Fatalf("wrong token: want 404, got %d", s)
	}
}

func TestPublishTakesADraftLive(t *testing.T) {
	staff, pub, id := draftEvent(t)
	token := previewToken(t, staff, id)

	if s, _, _ := pub.do("POST", "/api/v1/tournaments/"+id+"/publish", nil); s != 401 && s != 403 {
		t.Fatalf("anonymous publish: want 401/403, got %d", s)
	}
	if s, out, _ := staff.do("POST", "/api/v1/tournaments/"+id+"/publish", nil); s != 200 {
		t.Fatalf("publish: %d (%v)", s, out)
	}
	// Live, with registration open…
	if s, _, _ := pub.do("GET", "/api/v1/public/tournaments/"+id, nil); s != 200 {
		t.Fatalf("public page after publishing: want 200, got %d", s)
	}
	if s, out, _ := pub.do("POST", "/api/v1/public/tournaments/"+id+"/register", entry(nil)); s != 201 {
		t.Fatalf("register after publishing: %d (%v)", s, out)
	}
	// …and the preview link is dead.
	if s, _, _ := pub.do("GET", "/api/v1/public/tournaments/"+id+"/preview?preview="+token, nil); s != 404 {
		t.Fatalf("preview after publishing: want 404, got %d", s)
	}
	if s, _, _ := staff.do("POST", "/api/v1/tournaments/"+id+"/publish", nil); s != 409 {
		t.Fatalf("publishing twice: want 409, got %d", s)
	}
	if s, _, _ := staff.do("DELETE", "/api/v1/tournaments/"+id+"/draft", nil); s != 409 {
		t.Fatalf("discarding a live event: want 409, got %d", s)
	}
}

func TestDiscardingADraftTakesEverythingWithIt(t *testing.T) {
	staff, _, id := draftEvent(t)
	if s, out, _ := staff.do("DELETE", "/api/v1/tournaments/"+id+"/draft", nil); s != 200 {
		t.Fatalf("discard: %d (%v)", s, out)
	}
	if s, _, _ := staff.do("GET", "/api/v1/tournaments/"+id, nil); s != 404 {
		t.Fatalf("draft still there: %d", s)
	}
	_, _, rows := staff.do("GET", "/api/v1/tournament-categories", nil)
	for _, r := range rows {
		if r["tournament_id"] == id {
			t.Fatalf("the draft's categories were left behind")
		}
	}
}

func TestBannerLifecycle(t *testing.T) {
	staff, pub, id := draftEvent(t)
	token := previewToken(t, staff, id)

	upload := func(c *client, body []byte) int {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		fw, _ := mw.CreateFormFile("file", "banner.png")
		fw.Write(body)
		mw.Close()
		req, _ := http.NewRequest("POST", pub.srv.URL+"/api/v1/tournaments/"+id+"/banner", &buf)
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
	get := func(query string) (int, []byte) {
		res, err := http.Get(pub.srv.URL + "/api/v1/tournaments/" + id + "/banner" + query)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, b
	}
	png := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0}, 64)...)

	if got := upload(pub, png); got != 401 && got != 403 {
		t.Fatalf("anonymous upload: want 401/403, got %d", got)
	}
	// A PDF is a fine regulation and no use as a banner.
	if got := upload(staff, append([]byte("%PDF-1.4\n"), bytes.Repeat([]byte("a"), 64)...)); got != 415 {
		t.Fatalf("pdf as banner: want 415, got %d", got)
	}
	if got := upload(staff, png); got != 200 {
		t.Fatalf("upload: want 200, got %d", got)
	}

	// A draft's banner shows through its preview link only.
	if s, _ := get(""); s != 404 {
		t.Fatalf("draft banner, anonymous: want 404, got %d", s)
	}
	if s, b := get("?preview=" + token); s != 200 || !bytes.Equal(b, png) {
		t.Fatalf("draft banner, preview: %d, %d bytes", s, len(b))
	}
	_, page, _ := pub.do("GET", "/api/v1/public/tournaments/"+id+"/preview?preview="+token, nil)
	if tour, _ := page["tournament"].(map[string]any); tour["hasBanner"] != true {
		t.Fatalf("hasBanner not set: %v", page)
	}

	// Once live, anybody.
	staff.do("POST", "/api/v1/tournaments/"+id+"/publish", nil)
	if s, _ := get(""); s != 200 {
		t.Fatalf("live banner, anonymous: want 200, got %d", s)
	}
	_, row, _ := staff.do("GET", "/api/v1/tournaments/"+id, nil)
	if row["has_banner"] != true && row["has_banner"] != float64(1) {
		t.Fatalf("has_banner on the row: %v", row["has_banner"])
	}

	if s, _, _ := staff.do("DELETE", "/api/v1/tournaments/"+id+"/banner", nil); s != 200 {
		t.Fatalf("remove banner: %d", s)
	}
	if s, _ := get(""); s != 404 {
		t.Fatalf("removed banner: want 404, got %d", s)
	}
}
