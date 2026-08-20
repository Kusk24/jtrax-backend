// Round pages: art=2&rd=N, the "Pairings/Results" view Swiss-Manager uploads
// before a round is played and again after. This is the page a parent at the
// venue actually wants — which board their child is on and against whom —
// which the ranking view never says.
package chessresults

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Pairing is one board of one round, verbatim from the arbiter's table.
type Pairing struct {
	Board       int
	WhiteName   string
	WhiteRating int
	// BlackName keeps the site's own word for the special rows — "bye",
	// "not paired" — because inventing our own phrasing for the arbiter's
	// decision would be a translation, and translations drift.
	BlackName   string
	BlackRating int
	// Result as printed: "1 - 0", "½ - ½", "1" on a bye, "" before the game.
	Result string
}

// Round is one round's published pairings.
type Round struct {
	Number int
	// Date as the site prints it ("2026/08/14"); informational only.
	Date     string
	Pairings []Pairing
}

// ErrNoSuchRound means the site has not published this round. It is detected
// from the page heading, not the status code: asking for a round past the last
// one does not fail, the site silently serves the last round instead — the
// trap that makes the heading, not the URL, the authority on what was fetched.
var ErrNoSuchRound = errors.New("chessresults: that round is not published")

var (
	h3Pattern    = regexp.MustCompile(`(?is)<h3[^>]*>(.*?)</h3>`)
	roundHead    = regexp.MustCompile(`(?i)^round\s+(\d+)(?:\s+on\s+(\S+))?`)
	afterPattern = regexp.MustCompile(`(?i)after\s+(?:round\s+)?(\d+)`)
)

// PlayedRounds reads how many rounds a ranking heading has counted.
// "Rank after Round 4" → 4, "Final Ranking after 9 Rounds" → 9, "" → 0.
func PlayedRounds(stage string) int {
	m := afterPattern.FindStringSubmatch(stage)
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

// FinalStage reports whether a ranking heading says the event is over —
// after which the standings never change and neither do the rounds.
func FinalStage(stage string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(stage)), "final")
}

// FetchRound reads one round's pairing page, or ErrNoSuchRound when the site
// has nothing for that round yet.
func (c *Client) FetchRound(id, round int) (*Round, error) {
	page, err := c.get(fmt.Sprintf("%s/tnr%d.aspx?lan=1&art=2&rd=%d&zeilen=99999", c.base(), id, round))
	if err != nil {
		return nil, err
	}
	return parseRoundPage(page, round)
}

func parseRoundPage(page string, wanted int) (*Round, error) {
	r := &Round{}
	// The round heading is an <h3>, but not the only one — the site puts a
	// server-load notice in an <h3> too, so match by shape, not position.
	for _, m := range h3Pattern.FindAllStringSubmatch(page, -1) {
		if h := roundHead.FindStringSubmatch(cleanCell(m[1])); h != nil {
			r.Number, _ = strconv.Atoi(h[1])
			r.Date = h[2]
			break
		}
	}
	if r.Number != wanted {
		// Either no round heading at all (event page without pairings) or the
		// clamp served a different round than asked for.
		return nil, ErrNoSuchRound
	}

	table, ok := pairingTable(page)
	if !ok {
		return nil, ErrNoSuchRound
	}
	pairings, err := parsePairings(table)
	if err != nil {
		return nil, err
	}
	r.Pairings = pairings
	return r, nil
}

// pairingTable finds the CRs1 table whose header is a pairing header — board
// and result columns — rather than a ranking or player list.
func pairingTable(page string) (string, bool) {
	for _, m := range tablePattern.FindAllStringSubmatch(page, -1) {
		cols := headerAllColumns(m[1])
		if len(cols["bo."]) > 0 && len(cols["result"]) > 0 {
			return m[1], true
		}
	}
	return "", false
}

// headerAllColumns maps normalised header text to every index it appears at.
// The pairing header names two players, so "no.", "rtg" and "pts." each appear
// twice — the single-index map the ranking parser uses would keep only the
// black side's columns.
func headerAllColumns(table string) map[string][]int {
	rows := trPattern.FindAllStringSubmatch(table, 1)
	if len(rows) == 0 {
		return nil
	}
	out := map[string][]int{}
	for i, c := range cellPattern.FindAllStringSubmatch(rows[0][1], -1) {
		key := strings.ToLower(cleanCell(c[1]))
		if key != "" {
			out[key] = append(out[key], i)
		}
	}
	return out
}

func parsePairings(table string) ([]Pairing, error) {
	cols := headerAllColumns(table)
	first := func(key string) int {
		if idx := cols[key]; len(idx) > 0 {
			return idx[0]
		}
		return -1
	}
	// A side's rating is the first Rtg column after that side's name column.
	after := func(key string, col int) int {
		for _, i := range cols[key] {
			if i > col {
				return i
			}
		}
		return -1
	}
	boardCol, whiteCol, blackCol := first("bo."), first("white"), first("black")
	resultCol := first("result")
	if boardCol < 0 || whiteCol < 0 || blackCol < 0 || resultCol < 0 {
		return nil, errors.New("chessresults: pairing table is missing its columns — the markup has changed")
	}
	whiteRtg, blackRtg := after("rtg", whiteCol), after("rtg", blackCol)

	out := []Pairing{}
	for _, tr := range trPattern.FindAllStringSubmatch(table, -1)[1:] {
		cells := cellPattern.FindAllStringSubmatch(tr[1], -1)
		texts := make([]string, len(cells))
		for i, c := range cells {
			texts[i] = cleanCell(c[1])
		}
		board, err := strconv.Atoi(cell(texts, boardCol))
		if err != nil {
			// Group separators and repeated headers, same furniture as the
			// ranking table.
			continue
		}
		if cell(texts, whiteCol) == "" {
			continue
		}
		out = append(out, Pairing{
			Board:       board,
			WhiteName:   cell(texts, whiteCol),
			WhiteRating: atoiSafe(cell(texts, whiteRtg)),
			BlackName:   cell(texts, blackCol),
			BlackRating: atoiSafe(cell(texts, blackRtg)),
			Result:      cell(texts, resultCol),
		})
	}
	if len(out) == 0 {
		return nil, errors.New("chessresults: pairing table parsed to zero boards — the markup has changed")
	}
	return out, nil
}
