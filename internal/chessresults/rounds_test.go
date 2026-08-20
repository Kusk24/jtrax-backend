package chessresults

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

/* round_pairings.html is a real page saved from chess-results.com (the same
   event as final_ranking.html, round 4). round_upcoming.html is that page with
   the round heading advanced and the result cells emptied — the shape the site
   serves between the pairing upload and the results upload. */

func roundFixture(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestParsesARealRoundPage(t *testing.T) {
	r, err := parseRoundPage(roundFixture(t, "round_pairings.html"), 4)
	if err != nil {
		t.Fatal(err)
	}
	if r.Number != 4 || r.Date != "2026/08/14" {
		t.Fatalf("heading parsed as round %d on %q", r.Number, r.Date)
	}
	// The fixture's table has 15 boards, the last of which is a bye.
	if len(r.Pairings) != 15 {
		t.Fatalf("parsed %d boards, want 15", len(r.Pairings))
	}
	top := r.Pairings[0]
	if top.Board != 1 || top.WhiteName != "Rechmann, Peter" || top.WhiteRating != 2112 ||
		top.BlackName != "Bongardt, Livius" || top.BlackRating != 1899 || top.Result != "1 - 0" {
		t.Fatalf("board one parsed as %+v", top)
	}
	bye := r.Pairings[len(r.Pairings)-1]
	if bye.BlackName != "bye" || bye.Result != "1" {
		t.Fatalf("bye row parsed as %+v", bye)
	}
}

// The site never 404s a round: asking past the end silently serves the last
// round again. The heading is the only tell, and the parser must treat a
// mismatched heading as "not published", not as that round's data.
func TestAskingPastTheLastRoundIsNotThatRound(t *testing.T) {
	_, err := parseRoundPage(roundFixture(t, "round_pairings.html"), 5)
	if !errors.Is(err, ErrNoSuchRound) {
		t.Fatalf("round 5 request served round 4 data, err=%v", err)
	}
}

func TestParsesAnUpcomingRoundWithoutResults(t *testing.T) {
	r, err := parseRoundPage(roundFixture(t, "round_upcoming.html"), 5)
	if err != nil {
		t.Fatal(err)
	}
	played := 0
	for _, p := range r.Pairings {
		if p.Result != "" && p.BlackName != "bye" {
			played++
		}
	}
	if played != 0 {
		t.Fatalf("%d boards of an unplayed round carry results", played)
	}
	if r.Pairings[0].WhiteName == "" || r.Pairings[0].BlackName == "" {
		t.Fatal("pairings lost their names")
	}
}

func TestPlayedRounds(t *testing.T) {
	cases := map[string]int{
		"Rank after Round 4":         4,
		"Final Ranking after 9 Rounds": 9,
		"Final Ranking crosstable after 9 Rounds": 9,
		"": 0,
		"Starting rank": 0,
	}
	for stage, want := range cases {
		if got := PlayedRounds(stage); got != want {
			t.Errorf("PlayedRounds(%q) = %d, want %d", stage, got, want)
		}
	}
	if !FinalStage("Final Ranking after 9 Rounds") || FinalStage("Rank after Round 4") {
		t.Error("FinalStage misreads a heading")
	}
}
