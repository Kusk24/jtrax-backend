// Package standings turns a tournament's pairings into a table.
//
// Kept apart from the HTTP layer because this is the part that must be right:
// it decides who wins. A child goes home with a trophy or does not on the
// strength of these few functions, so they are pure, take no database and are
// tested against hand-worked examples.
package standings

import "sort"

// Result codes, matching the CHECK constraint on tournament_pairing.
const (
	Pending  = "Pending"
	WhiteWin = "1-0"
	BlackWin = "0-1"
	Draw     = "1/2-1/2"
	WhiteFF  = "+/-" // white wins by forfeit
	BlackFF  = "-/+" // black wins by forfeit
	Bye      = "bye"
)

// Pairing is one board in one round. Black is empty for a bye.
type Pairing struct {
	Round  int
	White  string
	Black  string
	Result string
}

// Row is one player's line in the table.
type Row struct {
	RegistrationID string  `json:"registrationId"`
	Points         float64 `json:"points"`
	Played         int     `json:"played"`
	Wins           int     `json:"wins"`
	Draws          int     `json:"draws"`
	Losses         int     `json:"losses"`
	Buchholz       float64 `json:"buchholz"`
	// Shared by players who are level on every tiebreak — joint third is a
	// real result and printing 3rd and 4th would be wrong.
	Rank int `json:"rank"`
}

// score returns what each side takes from a result, and whether a game was
// actually played.
//
// A forfeit and a bye both score a point without a game. That distinction is
// not pedantry: an unplayed point contributes nothing to an opponent's
// Buchholz, and a parent asking why their child has a point but no moves
// deserves an answer the data can give.
func score(result string) (white, black float64, played bool) {
	switch result {
	case WhiteWin:
		return 1, 0, true
	case BlackWin:
		return 0, 1, true
	case Draw:
		return 0.5, 0.5, true
	case WhiteFF:
		return 1, 0, false
	case BlackFF:
		return 0, 1, false
	case Bye:
		return 1, 0, false
	default: // Pending
		return 0, 0, false
	}
}

// Compute builds the table. `players` is every registration in the event, so
// somebody who has not played yet still appears on nil points rather than
// vanishing from their own tournament.
func Compute(players []string, pairings []Pairing) []Row {
	points := map[string]float64{}
	played := map[string]int{}
	wins := map[string]int{}
	draws := map[string]int{}
	losses := map[string]int{}
	opponents := map[string][]string{}
	for _, p := range players {
		points[p] = 0
	}

	for _, pr := range pairings {
		if pr.Result == Pending {
			continue
		}
		w, b, real := score(pr.Result)
		points[pr.White] += w
		if pr.Black != "" {
			points[pr.Black] += b
		}
		if !real {
			continue
		}
		played[pr.White]++
		opponents[pr.White] = append(opponents[pr.White], pr.Black)
		if pr.Black != "" {
			played[pr.Black]++
			opponents[pr.Black] = append(opponents[pr.Black], pr.White)
		}
		switch {
		case w > b:
			wins[pr.White]++
			losses[pr.Black]++
		case b > w:
			wins[pr.Black]++
			losses[pr.White]++
		default:
			draws[pr.White]++
			draws[pr.Black]++
		}
	}

	// Buchholz: the sum of the scores of the opponents actually faced. This is
	// the plain form, not FIDE's variants that substitute a virtual opponent
	// for byes — stated here because "Buchholz" alone names half a dozen
	// slightly different numbers.
	rows := make([]Row, 0, len(players))
	for _, p := range players {
		var bh float64
		for _, opp := range opponents[p] {
			bh += points[opp]
		}
		rows = append(rows, Row{
			RegistrationID: p, Points: points[p], Played: played[p],
			Wins: wins[p], Draws: draws[p], Losses: losses[p], Buchholz: bh,
		})
	}

	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if a.Points != b.Points {
			return a.Points > b.Points
		}
		if a.Buchholz != b.Buchholz {
			return a.Buchholz > b.Buchholz
		}
		if a.Wins != b.Wins {
			return a.Wins > b.Wins
		}
		// Last resort, so the same input always produces the same table.
		return a.RegistrationID < b.RegistrationID
	})

	for i := range rows {
		if i > 0 && level(rows[i-1], rows[i]) {
			rows[i].Rank = rows[i-1].Rank
			continue
		}
		rows[i].Rank = i + 1
	}
	return rows
}

func level(a, b Row) bool {
	return a.Points == b.Points && a.Buchholz == b.Buchholz && a.Wins == b.Wins
}

// Propose suggests pairings for the next round: sort by standing, then pair
// down the list, skipping a rematch where the field still allows one.
//
// This is a **convenience, not a pairing engine**. A real Swiss uses the Dutch
// system — colour balance, float history, score groups — and this does none of
// it. It exists so an arbiter starts from a sensible list instead of a blank
// screen, and every proposal is editable before it is saved.
func Propose(players []string, pairings []Pairing) []Pairing {
	table := Compute(players, pairings)
	met := map[string]bool{}
	for _, p := range pairings {
		if p.Black != "" {
			met[p.White+"|"+p.Black] = true
			met[p.Black+"|"+p.White] = true
		}
	}
	round := 1
	for _, p := range pairings {
		if p.Round >= round {
			round = p.Round + 1
		}
	}

	queue := make([]string, len(table))
	for i, r := range table {
		queue[i] = r.RegistrationID
	}
	out := []Pairing{}
	used := map[string]bool{}
	for i := 0; i < len(queue); i++ {
		if used[queue[i]] {
			continue
		}
		white := queue[i]
		used[white] = true
		// The nearest player below who has not met them yet.
		partner := ""
		for j := i + 1; j < len(queue); j++ {
			if used[queue[j]] || met[white+"|"+queue[j]] {
				continue
			}
			partner = queue[j]
			break
		}
		// Everyone left has already been played: take the next free player
		// anyway rather than refusing to pair the round.
		if partner == "" {
			for j := i + 1; j < len(queue); j++ {
				if !used[queue[j]] {
					partner = queue[j]
					break
				}
			}
		}
		if partner == "" {
			// Odd one out: a bye, which is a scored row, not a gap.
			out = append(out, Pairing{Round: round, White: white, Result: Bye})
			continue
		}
		used[partner] = true
		out = append(out, Pairing{Round: round, White: white, Black: partner, Result: Pending})
	}
	return out
}
