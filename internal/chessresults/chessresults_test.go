package chessresults_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Kusk24/jtrax-backend/internal/chessresults"
)

/* The fixtures are real pages saved from chess-results.com — a finished
   tournament's final ranking, its player list, and an event that has not
   started. Pinning real markup is the whole defence a scraper has: when the
   site changes, these tests are what says so before a parent's screen does. */

func fixture(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestParseRef(t *testing.T) {
	for input, want := range map[string]int{
		"https://chess-results.com/tnr1476156.aspx?lan=1":          1476156,
		"https://s2.chess-results.com/tnr1476156.aspx?lan=1&art=1": 1476156,
		"http://chess-results.com/TNR99.aspx":                      99,
		"1476156":                                                  1476156,
		"tnr1476156.aspx":                                          1476156,
		"/tnr1476156.aspx":                                         1476156,
		"":                                                         0,
		"https://example.com/tnr1476156.aspx":                      0, // wrong host
		"https://chess-results.com.evil.example/tnr1476156.aspx": 0, // suffix trick
		"https://chess-results.com/fed.aspx?lan=1&fed=THA":       0, // a federation page is not a tournament
		"not a url at all": 0,
	} {
		got, err := chessresults.ParseRef(input)
		if want == 0 {
			if err == nil {
				t.Errorf("ParseRef(%q) accepted, want rejection", input)
			}
			continue
		}
		if err != nil || got != want {
			t.Errorf("ParseRef(%q) = %d, %v; want %d", input, got, err, want)
		}
	}
}

// The wrong-host rejections above are the security case: this input reaches an
// endpoint that makes the *server* issue a GET, and accepting any host would
// turn the academy's backend into an open proxy aimed wherever a caller likes.
func TestParseRefRefusesForeignHosts(t *testing.T) {
	if _, err := chessresults.ParseRef("https://169.254.169.254/tnr1.aspx"); err == nil {
		t.Fatal("a metadata-service URL was accepted")
	}
}

func serveFixtures(t *testing.T, ranking, list string) *chessresults.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("art") {
		case "1":
			_, _ = w.Write([]byte(ranking))
		case "3":
			_, _ = w.Write([]byte(list))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return &chessresults.Client{BaseURL: srv.URL}
}

func TestFetchParsesARealFinalRanking(t *testing.T) {
	c := serveFixtures(t, fixture(t, "final_ranking.html"), fixture(t, "player_list.html"))
	tour, err := c.Fetch(1476156)
	if err != nil {
		t.Fatal(err)
	}
	if tour.Name != "BCCCasual 14.8.26" {
		t.Errorf("name = %q", tour.Name)
	}
	if tour.Stage != "Final Ranking after 9 Rounds" {
		t.Errorf("stage = %q", tour.Stage)
	}
	// The fixture's table has 33 player rows under one header row.
	if len(tour.Rows) != 33 {
		t.Fatalf("parsed %d rows, want 33", len(tour.Rows))
	}

	first := tour.Rows[0]
	if first.Rank != 1 || first.Name != "Rolston, Daniel Haruma" ||
		first.Federation != "JPN" || first.Rating != 1960 || first.Points != 7 {
		t.Errorf("first row = %+v", first)
	}
	// "6,5" is six and a half: the site writes decimals with a comma, and a
	// parser that missed this would floor every half point in the standings.
	if third := tour.Rows[2]; third.Points != 6.5 {
		t.Errorf("rank 3 points = %v, want 6.5 (comma decimal)", third.Points)
	}
}

// The ranking view has no FideID column; the player list does. The IDs must
// travel across the name join, because they are how students are recognised.
func TestFetchFillsFideIDsFromThePlayerList(t *testing.T) {
	c := serveFixtures(t, fixture(t, "final_ranking.html"), fixture(t, "player_list.html"))
	tour, err := c.Fetch(1476156)
	if err != nil {
		t.Fatal(err)
	}
	var pirat *chessresults.Row
	for i := range tour.Rows {
		if strings.Contains(tour.Rows[i].Name, "Bunnag") {
			pirat = &tour.Rows[i]
		}
	}
	if pirat == nil {
		t.Fatal("Bunnag, Pirat not found in the ranking")
	}
	if pirat.FideID != "6206638" {
		t.Errorf("FideID = %q, want 6206638 carried over from the player list", pirat.FideID)
	}
}

// An event that has not started serves a registration page at the same URL.
// That is a trackable tournament with no standings, not an error.
func TestFetchHandlesANotStartedTournament(t *testing.T) {
	c := serveFixtures(t, fixture(t, "not_started.html"), fixture(t, "player_list.html"))
	tour, err := c.Fetch(1365480)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tour.Name, "Blue Chevaliers") {
		t.Errorf("name = %q", tour.Name)
	}
	if tour.Stage != "" || len(tour.Rows) != 0 {
		t.Errorf("a not-started event must have no stage and no rows, got stage=%q rows=%d",
			tour.Stage, len(tour.Rows))
	}
}

// A page that has a ranking-shaped table but no parsable players must fail
// loudly. Shipping an empty standings table as if it were the truth is the
// scraper failure mode this package promises not to have.
func TestFetchFailsLoudlyOnChangedMarkup(t *testing.T) {
	broken := `<h2>Some Tournament</h2><h2>Final Ranking after 5 Rounds</h2>
	<table class="CRs1"><tr><th>Rk.</th><th>Name</th></tr>
	<tr><td>not-a-rank</td><td></td></tr></table>`
	c := serveFixtures(t, broken, "")
	if _, err := c.Fetch(1); err == nil {
		t.Fatal("a table that parsed to zero players was accepted")
	}
}

func TestNormalizeName(t *testing.T) {
	for input, want := range map[string]string{
		"Somchai, Niran":         "niran somchai",
		"  niran   SOMCHAI ":     "niran somchai",
		"Rolston, Daniel Haruma": "daniel haruma rolston",
		"Penny":                  "penny",
	} {
		if got := chessresults.NormalizeName(input); got != want {
			t.Errorf("NormalizeName(%q) = %q, want %q", input, got, want)
		}
	}
	if chessresults.NormalizeName("Somchai, Niran") != chessresults.NormalizeName("niran somchai") {
		t.Error("comma order and plain order must normalise to the same key")
	}
}

func TestParseRefErrorIsRecognisable(t *testing.T) {
	_, err := chessresults.ParseRef("https://example.com/x")
	if !errors.Is(err, chessresults.ErrNotATournamentURL) {
		t.Fatalf("err = %v, want ErrNotATournamentURL", err)
	}
}

// The fixture contains a real player whose rank cell is blank — the site's
// rendering of a shared/unranked row. They must appear in the parse, carrying
// the previous rank, not vanish from the standings.
func TestBlankRankRowsAreKeptNotDropped(t *testing.T) {
	c := serveFixtures(t, fixture(t, "final_ranking.html"), fixture(t, "player_list.html"))
	tour, err := c.Fetch(1476156)
	if err != nil {
		t.Fatal(err)
	}
	var pisut *chessresults.Row
	for i := range tour.Rows {
		if strings.HasPrefix(tour.Rows[i].Name, "Pisut") {
			pisut = &tour.Rows[i]
		}
	}
	if pisut == nil {
		t.Fatal("the blank-rank player was dropped from the standings")
	}
	if pisut.Rank != 31 {
		t.Errorf("blank rank should carry the previous rank 31, got %d", pisut.Rank)
	}
}
