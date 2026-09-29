package chessresults_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Kusk24/jtrax-backend/internal/chessresults"
)

/* The event's sections, read from the details page. The fixtures are the real
   WCIB CHESS CHAMPIONSHIP 2025 [U14 + G14] (tnr1193905) as the site served it
   on 28 Sep 2026: first the archived page with its links hidden behind the
   "Show tournament details" button, then the page after pressing it. */

// archiveSite answers a GET with the gated page and a POST carrying the
// details button with the full one — the real site's behaviour for an event
// that ended more than five days ago.
func archiveSite(t *testing.T) (*httptest.Server, *int) {
	t.Helper()
	gated, details := fixture(t, "event_gated.html"), fixture(t, "event_details.html")
	posts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			_ = r.ParseForm()
			if r.PostForm.Get("cb_alleDetails") == "" || r.PostForm.Get("__VIEWSTATE") == "" {
				t.Errorf("the details button was not posted back with the page's own form")
			}
			posts++
			fmt.Fprint(w, details)
			return
		}
		fmt.Fprint(w, gated)
	}))
	t.Cleanup(srv.Close)
	return srv, &posts
}

func TestFetchEventFindsEverySection(t *testing.T) {
	srv, posts := archiveSite(t)
	c := &chessresults.Client{BaseURL: srv.URL}

	ev, err := c.FetchEvent(1193905)
	if err != nil {
		t.Fatal(err)
	}
	if *posts != 1 {
		t.Fatalf("want the details button pressed once, got %d", *posts)
	}
	if ev.Name != "WCIB CHESS CHAMPIONSHIP 2025" {
		t.Fatalf("event name: %q", ev.Name)
	}
	if ev.Rounds != 7 {
		t.Fatalf("rounds: want 7, got %d", ev.Rounds)
	}
	want := []chessresults.Section{
		{ID: 1193901, Name: "U06 + G06"},
		{ID: 1193902, Name: "U08 + G08"},
		{ID: 1193903, Name: "U10 + G10"},
		{ID: 1193904, Name: "U12 + G12"},
		{ID: 1193905, Name: "U14 + G14"}, // the one pasted, printed bold rather than linked
		{ID: 1193906, Name: "U18 + G18"},
	}
	if len(ev.Sections) != len(want) {
		t.Fatalf("sections: %+v", ev.Sections)
	}
	for i := range want {
		if ev.Sections[i] != want[i] {
			t.Fatalf("section %d: want %+v, got %+v", i, want[i], ev.Sections[i])
		}
	}
}

// chess-results.com sends every visitor on to one of its servers (S2, S3…).
// The button has to be posted there: posting to the first address is
// redirected again, arrives as a GET, and only the one pasted section is found.
func TestFetchEventPostsToTheServerItWasSentTo(t *testing.T) {
	node, posts := archiveSite(t)
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, node.URL+r.URL.RequestURI(), http.StatusFound)
	}))
	t.Cleanup(front.Close)

	ev, err := (&chessresults.Client{BaseURL: front.URL}).FetchEvent(1193905)
	if err != nil {
		t.Fatal(err)
	}
	if *posts != 1 || len(ev.Sections) != 6 || ev.Rounds != 7 {
		t.Fatalf("want 6 sections and 7 rounds after one post, got %d sections, %d rounds, %d posts", len(ev.Sections), ev.Rounds, *posts)
	}
}

// A live event shows its details without the button; nothing is posted.
func TestFetchEventReadsAnOpenPageDirectly(t *testing.T) {
	details := fixture(t, "event_details.html")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("posted to a page that had no button")
		}
		fmt.Fprint(w, details)
	}))
	t.Cleanup(srv.Close)

	ev, err := (&chessresults.Client{BaseURL: srv.URL}).FetchEvent(1193905)
	if err != nil || len(ev.Sections) != 6 {
		t.Fatalf("want 6 sections, got %v (%v)", ev, err)
	}
}

// An organiser who never grouped the sections leaves no selection row: the
// event is the one section the link pointed at, named from its title.
func TestFetchEventWithoutSelectionIsOneSection(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<h2>Bangkok Rapid 2026 [Open]</h2>
			<table><tr><td class="CR">Number of rounds</td><td class="CR">5</td></tr></table>`)
	}))
	t.Cleanup(srv.Close)

	ev, err := (&chessresults.Client{BaseURL: srv.URL}).FetchEvent(777)
	if err != nil {
		t.Fatal(err)
	}
	if ev.Name != "Bangkok Rapid 2026" || ev.Rounds != 5 {
		t.Fatalf("event: %+v", ev)
	}
	if len(ev.Sections) != 1 || ev.Sections[0] != (chessresults.Section{ID: 777, Name: "Open"}) {
		t.Fatalf("sections: %+v", ev.Sections)
	}
}

func TestFetchEventRefusesAPageThatIsNotATournament(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, strings.Repeat("<p>maintenance</p>", 3))
	}))
	t.Cleanup(srv.Close)
	if _, err := (&chessresults.Client{BaseURL: srv.URL}).FetchEvent(1); err == nil {
		t.Fatal("want an error for a page with no tournament heading")
	}
}
