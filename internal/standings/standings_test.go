package standings_test

import (
	"testing"

	"github.com/Kusk24/jtrax-backend/internal/standings"
)

func byID(rows []standings.Row) map[string]standings.Row {
	out := map[string]standings.Row{}
	for _, r := range rows {
		out[r.RegistrationID] = r
	}
	return out
}

// A hand-worked three-round event. Every number below was calculated on paper
// first; if the code disagrees, the code is wrong.
//
//	R1: A beat B, C drew D
//	R2: A drew C, D beat B
//	R3: A beat D, C beat B
//
//	A = 1 + 0.5 + 1   = 2.5
//	C = 0.5 + 0.5 + 1 = 2.0
//	D = 0.5 + 1 + 0   = 1.5
//	B = 0 + 0 + 0     = 0.0
func threeRounds() ([]string, []standings.Pairing) {
	return []string{"A", "B", "C", "D"}, []standings.Pairing{
		{Round: 1, White: "A", Black: "B", Result: standings.WhiteWin},
		{Round: 1, White: "C", Black: "D", Result: standings.Draw},
		{Round: 2, White: "A", Black: "C", Result: standings.Draw},
		{Round: 2, White: "D", Black: "B", Result: standings.WhiteWin},
		{Round: 3, White: "A", Black: "D", Result: standings.WhiteWin},
		{Round: 3, White: "C", Black: "B", Result: standings.WhiteWin},
	}
}

func TestPointsAndOrder(t *testing.T) {
	players, pairings := threeRounds()
	rows := standings.Compute(players, pairings)

	want := []struct {
		id     string
		points float64
		rank   int
	}{{"A", 2.5, 1}, {"C", 2.0, 2}, {"D", 1.5, 3}, {"B", 0, 4}}
	if len(rows) != len(want) {
		t.Fatalf("got %d rows, want %d", len(rows), len(want))
	}
	for i, w := range want {
		if rows[i].RegistrationID != w.id || rows[i].Points != w.points || rows[i].Rank != w.rank {
			t.Errorf("row %d = %+v, want %s on %.1f at rank %d", i, rows[i], w.id, w.points, w.rank)
		}
	}
}

func TestWinDrawLossCounts(t *testing.T) {
	players, pairings := threeRounds()
	got := byID(standings.Compute(players, pairings))

	// A: won two, drew one, lost none.
	if a := got["A"]; a.Wins != 2 || a.Draws != 1 || a.Losses != 0 || a.Played != 3 {
		t.Errorf("A = %+v", a)
	}
	// B: lost all three.
	if b := got["B"]; b.Wins != 0 || b.Draws != 0 || b.Losses != 3 {
		t.Errorf("B = %+v", b)
	}
}

// Buchholz is the sum of the opponents' final scores. D and B both finish
// behind C, so this is what separates players level on points.
func TestBuchholz(t *testing.T) {
	players, pairings := threeRounds()
	got := byID(standings.Compute(players, pairings))

	// A played B (0), C (2.0), D (1.5) -> 3.5
	if got["A"].Buchholz != 3.5 {
		t.Errorf("A Buchholz = %v, want 3.5", got["A"].Buchholz)
	}
	// B played A (2.5), D (1.5), C (2.0) -> 6.0
	if got["B"].Buchholz != 6.0 {
		t.Errorf("B Buchholz = %v, want 6.0", got["B"].Buchholz)
	}
}

// Players level on points must be separated by Buchholz, not by whoever the
// database happened to return first.
//
//	R1: A beat C, B beat D
//	R2: C beat D
//
// A, B and C all finish on 1. A beat C (who ends on 1); B beat D (who ends on
// 0). So A's Buchholz is 1 and B's is 0, and B must drop below both.
func TestBuchholzBreaksATie(t *testing.T) {
	players := []string{"A", "B", "C", "D"}
	rows := standings.Compute(players, []standings.Pairing{
		{Round: 1, White: "A", Black: "C", Result: standings.WhiteWin},
		{Round: 1, White: "B", Black: "D", Result: standings.WhiteWin},
		{Round: 2, White: "C", Black: "D", Result: standings.WhiteWin},
	})
	got := byID(rows)

	for _, id := range []string{"A", "B", "C"} {
		if got[id].Points != 1 {
			t.Fatalf("%s should be on 1 point, got %v", id, got[id].Points)
		}
	}
	if got["A"].Buchholz != 1 || got["B"].Buchholz != 0 {
		t.Fatalf("Buchholz: A=%v B=%v, want 1 and 0", got["A"].Buchholz, got["B"].Buchholz)
	}
	// The point of the tiebreak: B is level on points but ranked below.
	if !(got["B"].Rank > got["A"].Rank && got["B"].Rank > got["C"].Rank) {
		t.Errorf("B (Buchholz 0) ranked %d, A %d, C %d — B should be last of the three",
			got["B"].Rank, got["A"].Rank, got["C"].Rank)
	}
	// A and C are level on every tiebreak, so they genuinely share a rank.
	if got["A"].Rank != got["C"].Rank {
		t.Errorf("A and C are inseparable but ranked %d and %d", got["A"].Rank, got["C"].Rank)
	}
}

// Genuinely inseparable players share a rank. Printing 3rd and 4th would
// invent a difference that does not exist.
func TestTrulyLevelPlayersShareARank(t *testing.T) {
	players := []string{"A", "B", "C", "D"}
	pairings := []standings.Pairing{
		{Round: 1, White: "A", Black: "B", Result: standings.Draw},
		{Round: 1, White: "C", Black: "D", Result: standings.Draw},
	}
	rows := standings.Compute(players, pairings)
	for _, r := range rows {
		if r.Points != 0.5 || r.Rank != 1 {
			t.Fatalf("everyone drew, so all should be joint 1st on 0.5: %+v", r)
		}
	}
}

func TestByeScoresAPointButIsNotAGamePlayed(t *testing.T) {
	players := []string{"A", "B", "C"}
	pairings := []standings.Pairing{
		{Round: 1, White: "A", Black: "B", Result: standings.WhiteWin},
		{Round: 1, White: "C", Result: standings.Bye},
	}
	got := byID(standings.Compute(players, pairings))
	if got["C"].Points != 1 {
		t.Errorf("a bye is a full point, got %v", got["C"].Points)
	}
	if got["C"].Played != 0 || got["C"].Wins != 0 {
		t.Errorf("a bye is not a game played or a win: %+v", got["C"])
	}
	// And it must not inflate anyone's Buchholz, since there was no opponent.
	if got["C"].Buchholz != 0 {
		t.Errorf("a bye has no opponent to count: %v", got["C"].Buchholz)
	}
}

func TestForfeitScoresButIsNotPlayed(t *testing.T) {
	players := []string{"A", "B"}
	got := byID(standings.Compute(players, []standings.Pairing{
		{Round: 1, White: "A", Black: "B", Result: standings.WhiteFF},
	}))
	if got["A"].Points != 1 || got["B"].Points != 0 {
		t.Errorf("forfeit points wrong: A=%v B=%v", got["A"].Points, got["B"].Points)
	}
	if got["A"].Played != 0 || got["A"].Wins != 0 {
		t.Errorf("a forfeit is not a game played: %+v", got["A"])
	}
}

func TestPendingResultsScoreNothing(t *testing.T) {
	players := []string{"A", "B"}
	got := byID(standings.Compute(players, []standings.Pairing{
		{Round: 1, White: "A", Black: "B", Result: standings.Pending},
	}))
	if got["A"].Points != 0 || got["B"].Points != 0 || got["A"].Played != 0 {
		t.Errorf("an unplayed board must not score: %+v %+v", got["A"], got["B"])
	}
}

// A player who has not been paired yet still belongs in their own tournament.
func TestUnpairedPlayersStillAppear(t *testing.T) {
	rows := standings.Compute([]string{"A", "B", "C"}, []standings.Pairing{
		{Round: 1, White: "A", Black: "B", Result: standings.WhiteWin},
	})
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want 3", len(rows))
	}
	if got := byID(rows)["C"]; got.Points != 0 || got.Rank == 0 {
		t.Errorf("C should be listed on nil points with a rank: %+v", got)
	}
}

/* ---- proposals ---- */

func TestProposeAvoidsARematch(t *testing.T) {
	players := []string{"A", "B", "C", "D"}
	round1 := []standings.Pairing{
		{Round: 1, White: "A", Black: "B", Result: standings.WhiteWin},
		{Round: 1, White: "C", Black: "D", Result: standings.WhiteWin},
	}
	got := standings.Propose(players, round1)
	if len(got) != 2 {
		t.Fatalf("want 2 boards, got %d", len(got))
	}
	for _, p := range got {
		if p.Round != 2 {
			t.Errorf("proposal should be for round 2, got %d", p.Round)
		}
		if (p.White == "A" && p.Black == "B") || (p.White == "B" && p.Black == "A") ||
			(p.White == "C" && p.Black == "D") || (p.White == "D" && p.Black == "C") {
			t.Errorf("proposed a rematch: %s vs %s", p.White, p.Black)
		}
	}
	// Leaders meet: A and C both won.
	if !(got[0].White == "A" && got[0].Black == "C") {
		t.Errorf("top board = %s vs %s, want the two winners", got[0].White, got[0].Black)
	}
}

func TestProposeGivesTheOddPlayerABye(t *testing.T) {
	got := standings.Propose([]string{"A", "B", "C"}, nil)
	byes := 0
	for _, p := range got {
		if p.Result == standings.Bye {
			byes++
			if p.Black != "" {
				t.Errorf("a bye has no opponent: %+v", p)
			}
		}
	}
	if byes != 1 {
		t.Errorf("three players is one bye, got %d", byes)
	}
}

// Pairing everyone against everyone leaves no fresh opponents; the round must
// still be paired rather than refused.
func TestProposeStillPairsWhenEveryoneHasMet(t *testing.T) {
	players := []string{"A", "B"}
	played := []standings.Pairing{{Round: 1, White: "A", Black: "B", Result: standings.Draw}}
	got := standings.Propose(players, played)
	if len(got) != 1 || got[0].Black == "" {
		t.Fatalf("want one board with two players, got %+v", got)
	}
}
