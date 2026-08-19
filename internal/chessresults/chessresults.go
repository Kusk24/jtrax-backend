// Package chessresults reads tournament standings from chess-results.com.
//
// chess-results.com is where most arbiters in the region publish: it is the
// output side of Swiss-Manager, the desktop program a tournament is actually
// run with. Two facts shape everything here:
//
//   - **Nobody can write to it.** There is no upload API — the arbiter's
//     desktop program is the only way in. So this package only reads, and the
//     product built on it is "track their tournament here", never "publish our
//     tournament there".
//   - **There is no read API either.** The site serves server-rendered HTML,
//     which means this is a scraper, with a scraper's honesty problem: it
//     works until the day the markup changes. The tables are machine-generated
//     by one program and have been stable for years, the parsing is driven by
//     the header row rather than column positions, and the tests pin real
//     saved pages — but when it breaks, it should break loudly, not by
//     shipping half a table.
//
// Politeness matters: the site is run on donations and blocks abusive bots.
// One refresh costs at most two page fetches, results are stored rather than
// proxied, and the caller enforces a minimum interval between refreshes.
package chessresults

import (
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// DefaultBaseURL is the public site. Overridden in tests, and by deployments
// behind an egress proxy.
const DefaultBaseURL = "https://chess-results.com"

// maxPageBytes caps one fetched page. Ranking pages run tens of kilobytes; a
// response past this is not a chess tournament.
const maxPageBytes = 4 << 20

// Row is one line of a ranking table.
type Row struct {
	Rank       int
	Name       string
	FideID     string
	Federation string
	Rating     int
	Points     float64
	Club       string
}

// Tournament is what one fetch learns about an event.
type Tournament struct {
	ID   int
	Name string
	// Stage is the site's own heading over the table — "Final Ranking after 9
	// Rounds", "Rank after Round 4" — kept verbatim because it is the honest
	// statement of how final the numbers are. Empty when the event has not
	// started and there is no ranking at all.
	Stage string
	Rows  []Row
}

// tnrPattern matches the tournament number in a chess-results URL path.
var tnrPattern = regexp.MustCompile(`(?i)^/?tnr(\d+)\.aspx$`)

// ErrNotATournamentURL means the input could not be read as a chess-results
// tournament reference.
var ErrNotATournamentURL = errors.New("chessresults: not a chess-results.com tournament link")

// ParseRef extracts the tournament number from whatever a person pastes: the
// full URL (any mirror — s1., s2. and so on serve the same content), just the
// path, or the bare number itself.
func ParseRef(s string) (int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, ErrNotATournamentURL
	}
	// A bare number is the friendliest input to accept: it is what remains
	// when someone trims a URL by hand.
	if n, err := strconv.Atoi(s); err == nil && n > 0 {
		return n, nil
	}
	u, err := url.Parse(s)
	if err != nil {
		return 0, ErrNotATournamentURL
	}
	if u.Host != "" {
		host := strings.ToLower(u.Hostname())
		// Mirrors are s1.chess-results.com etc.; anything else is someone
		// pasting the wrong link, and fetching an arbitrary host with our
		// server would turn this endpoint into a proxy.
		if host != "chess-results.com" && !strings.HasSuffix(host, ".chess-results.com") {
			return 0, ErrNotATournamentURL
		}
	}
	m := tnrPattern.FindStringSubmatch(u.Path)
	if m == nil {
		return 0, ErrNotATournamentURL
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n <= 0 {
		return 0, ErrNotATournamentURL
	}
	return n, nil
}

// Client fetches tournament pages.
type Client struct {
	BaseURL string
	HTTP    *http.Client
}

func New() *Client {
	return &Client{BaseURL: DefaultBaseURL, HTTP: &http.Client{Timeout: 25 * time.Second}}
}

func (c *Client) base() string {
	if c.BaseURL == "" {
		return DefaultBaseURL
	}
	return c.BaseURL
}

func (c *Client) http() *http.Client {
	if c.HTTP == nil {
		return &http.Client{Timeout: 25 * time.Second}
	}
	return c.HTTP
}

// Fetch reads one tournament: the ranking view, plus the player list when the
// ranking carries no FIDE IDs of its own.
//
// lan=1 forces English headers — the parse is header-driven, and the Thai
// headings are not the contract the tests pin. zeilen=99999 asks for every row
// on one page instead of pagination.
func (c *Client) Fetch(id int) (*Tournament, error) {
	page, err := c.get(fmt.Sprintf("%s/tnr%d.aspx?lan=1&art=1&flag=30&zeilen=99999", c.base(), id))
	if err != nil {
		return nil, err
	}
	t := &Tournament{ID: id}
	t.Name, t.Stage = headings(page)
	if t.Name == "" {
		return nil, fmt.Errorf("chessresults: page for %d has no tournament heading", id)
	}

	table, ok := rankingTable(page)
	if ok {
		rows, err := parseRanking(table)
		if err != nil {
			return nil, err
		}
		t.Rows = rows
	} else {
		// No ranking yet — the event has not started, and art=1 is serving the
		// registration view. Tracked anyway: the whole point of tracking is
		// that the refresh after round one finds the table.
		t.Stage = ""
	}

	// The ranking view usually has no FideID column; the player list does.
	// Names are the join between the two pages, which is safe because both are
	// generated from the same Swiss-Manager database.
	if len(t.Rows) > 0 && missingFideIDs(t.Rows) {
		if list, err := c.get(fmt.Sprintf("%s/tnr%d.aspx?lan=1&art=3&flag=30&zeilen=99999", c.base(), id)); err == nil {
			applyFideIDs(t.Rows, list)
		}
		// A failed second fetch is not a failed tournament: the standings
		// stand on their own, only the student matching gets weaker.
	}
	return t, nil
}

func (c *Client) get(u string) (string, error) {
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	// A plain browser agent. The site's robots rules single out scrapers that
	// hammer it; a school checking one tournament a few times a day is the
	// traffic the site exists for.
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; JTrax school portal)")
	res, err := c.http().Do(req)
	if err != nil {
		return "", fmt.Errorf("chessresults: unreachable: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("chessresults: status %d", res.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, maxPageBytes))
	if err != nil {
		return "", fmt.Errorf("chessresults: read: %w", err)
	}
	return string(raw), nil
}

/* ---- parsing ----

   The site is ASP.NET server-rendered and has no API, so this is string
   surgery over HTML. Kept survivable three ways: everything anchors on the
   CRs1 table class Swiss-Manager has emitted for years, columns are found by
   header text rather than position, and a table that does not match returns an
   error instead of a partial answer. */

var (
	h2Pattern    = regexp.MustCompile(`(?is)<h2[^>]*>(.*?)</h2>`)
	tablePattern = regexp.MustCompile(`(?is)<table[^>]*class="CRs1"[^>]*>(.*?)</table>`)
	trPattern    = regexp.MustCompile(`(?is)<tr[^>]*>(.*?)</tr>`)
	cellPattern  = regexp.MustCompile(`(?is)<t[hd][^>]*>(.*?)</t[hd]>`)
	tagPattern   = regexp.MustCompile(`(?s)<[^>]*>`)
)

// headings returns the tournament name and the table's own caption. The site
// renders the event name as the first <h2> and the view's heading ("Final
// Ranking after 9 Rounds", "Starting rank") as the second.
func headings(page string) (name, stage string) {
	hs := h2Pattern.FindAllStringSubmatch(page, 2)
	if len(hs) > 0 {
		name = cleanCell(hs[0][1])
	}
	if len(hs) > 1 {
		s := cleanCell(hs[1][1])
		low := strings.ToLower(s)
		// Only ranking headings count as a stage. "Starting rank" and the
		// registration views also come through this heading, and neither is a
		// statement about standings.
		if strings.Contains(low, "ranking") || strings.HasPrefix(low, "rank after") {
			stage = s
		}
	}
	return name, stage
}

// rankingTable finds the standings table: the CRs1 table whose header row
// starts with a rank column. The same class is used for the starting-rank and
// player lists, so the class alone is not enough.
func rankingTable(page string) (string, bool) {
	for _, m := range tablePattern.FindAllStringSubmatch(page, -1) {
		cols := headerColumns(m[1])
		if _, ok := cols["rk."]; ok {
			return m[1], true
		}
	}
	return "", false
}

// headerColumns maps normalised header text to column index.
func headerColumns(table string) map[string]int {
	rows := trPattern.FindAllStringSubmatch(table, 1)
	if len(rows) == 0 {
		return nil
	}
	out := map[string]int{}
	for i, c := range cellPattern.FindAllStringSubmatch(rows[0][1], -1) {
		key := strings.ToLower(cleanCell(c[1]))
		if key != "" {
			out[key] = i
		}
	}
	return out
}

func parseRanking(table string) ([]Row, error) {
	cols := headerColumns(table)
	nameCol := colIndex(cols, "name")
	if nameCol < 0 {
		return nil, errors.New("chessresults: ranking table has no Name column — the markup has changed")
	}
	rankCol := colIndex(cols, "rk.")
	fedCol := colIndex(cols, "fed")
	clubCol := colIndex(cols, "club/city")
	fideCol := colIndex(cols, "fideid")
	rtgCol := colIndex(cols, "rtg")
	ptsCol := colIndex(cols, "pts.")

	rows := trPattern.FindAllStringSubmatch(table, -1)
	out := []Row{}
	lastRank := 0
	for _, tr := range rows[1:] {
		cells := cellPattern.FindAllStringSubmatch(tr[1], -1)
		texts := make([]string, len(cells))
		for i, c := range cells {
			texts[i] = cleanCell(c[1])
		}
		name := cell(texts, nameCol)
		if name == "" {
			// Group separators and repeated headers are furniture, not players.
			continue
		}
		rank, err := strconv.Atoi(cell(texts, rankCol))
		if err != nil {
			// A real player with a blank rank cell: the site blanks the rank
			// on shared and unranked rows. Carrying the previous rank keeps
			// the player in the table, which matters more than the number —
			// dropping a child from the standings because they tied is the
			// kind of bug a parent notices before we do.
			rank = lastRank
		}
		lastRank = rank
		out = append(out, Row{
			Rank:       rank,
			Name:       name,
			Federation: cell(texts, fedCol),
			Club:       cell(texts, clubCol),
			FideID:     cell(texts, fideCol),
			Rating:     atoiSafe(cell(texts, rtgCol)),
			Points:     decimal(cell(texts, ptsCol)),
		})
	}
	if len(out) == 0 {
		return nil, errors.New("chessresults: ranking table parsed to zero players — the markup has changed")
	}
	return out, nil
}

// applyFideIDs fills missing FIDE IDs from the player-list page, joined on
// the player's name.
func applyFideIDs(rows []Row, listPage string) {
	byName := map[string]string{}
	for _, m := range tablePattern.FindAllStringSubmatch(listPage, -1) {
		cols := headerColumns(m[1])
		nameCol, hasName := cols["name"]
		fideCol, hasFide := cols["fideid"]
		if !hasName || !hasFide {
			continue
		}
		for _, tr := range trPattern.FindAllStringSubmatch(m[1], -1)[1:] {
			cells := cellPattern.FindAllStringSubmatch(tr[1], -1)
			texts := make([]string, len(cells))
			for i, c := range cells {
				texts[i] = cleanCell(c[1])
			}
			name := NormalizeName(cell(texts, nameCol))
			if id := cell(texts, fideCol); name != "" && id != "" && id != "0" {
				byName[name] = id
			}
		}
	}
	for i := range rows {
		if rows[i].FideID == "" || rows[i].FideID == "0" {
			rows[i].FideID = byName[NormalizeName(rows[i].Name)]
		}
	}
}

func missingFideIDs(rows []Row) bool {
	for _, r := range rows {
		if r.FideID == "" || r.FideID == "0" {
			return true
		}
	}
	return false
}

// NormalizeName flattens a player name for matching: lower-cased, single
// spaces, no comma order. "Somchai, Niran" and "niran somchai" meet in the
// middle. Exposed because the API's student matching needs the same rule —
// two normalisers that drift apart is how matching quietly stops working.
func NormalizeName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if head, tail, found := strings.Cut(s, ","); found {
		s = strings.TrimSpace(tail) + " " + strings.TrimSpace(head)
	}
	return strings.Join(strings.Fields(s), " ")
}

/* ---- small helpers ---- */

// colIndex returns a column's index, or -1 when the header does not have it.
//
// The distinction matters: a plain map lookup of a missing key returns 0,
// which is the *rank column*. The first version of this package did exactly
// that, and every tournament whose ranking view lacked a FideID column got
// its players' ranks stored as their FIDE IDs.
func colIndex(cols map[string]int, key string) int {
	if i, ok := cols[key]; ok {
		return i
	}
	return -1
}

func cell(texts []string, i int) string {
	if i < 0 || i >= len(texts) {
		return ""
	}
	return texts[i]
}

func cleanCell(s string) string {
	s = tagPattern.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	// The first argument is U+00A0: &nbsp; unescapes to it, and the site pads
	// header cells with them.
	s = strings.ReplaceAll(s, " ", " ")
	return strings.Join(strings.Fields(s), " ")
}

func atoiSafe(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

// decimal parses the site's numbers, which use a comma for the decimal point:
// "6,5" is six and a half.
func decimal(s string) float64 {
	f, _ := strconv.ParseFloat(strings.ReplaceAll(s, ",", "."), 64)
	return f
}
