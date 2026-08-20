// External tournaments tracked from chess-results.com.
//
// The academy's students play in other people's tournaments, and those are
// published on chess-results.com by the arbiter's Swiss-Manager. Nothing can
// write to that site — not us, not anyone — so this is strictly a read: staff
// paste a tournament link, the server pulls the standings, recognises which
// rows are our students, and keeps a copy everyone signed in can see.
//
// Stored, not proxied. The site is slow, sometimes down, and run on donations;
// a class of parents refreshing during a tournament must hit our database, not
// their server. Staleness is handled two ways: staff can refresh by hand
// (throttled), and a read of an unfinished tournament refreshes automatically
// once the copy is old enough.
package api

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/Kusk24/jtrax-backend/internal/chessresults"
	"github.com/Kusk24/jtrax-backend/internal/httpx"
)

// externalSyncInterval is how stale a copy of an *unfinished* tournament may
// be before a read refreshes it. The academy's own workflow is Swiss-Manager →
// upload to chess-results after every round: pairings land minutes before play
// and results minutes after, and a parent at the venue is refreshing exactly
// then. Three minutes keeps the page live through a round turnaround while the
// floor below keeps the site's cost bounded — and a finished event never
// refreshes at all.
const externalSyncInterval = 3 * time.Minute

// externalRefreshFloor is the minimum gap between fetches of one tournament,
// however they are triggered. This is the politeness budget: the site is a
// donation-run service that bans scrapers, and a stuck "Refresh" clicker must
// not be able to aim a request a second at it.
const externalRefreshFloor = 60 * time.Second

type chessResultsDeps struct {
	db     *sql.DB
	client *chessresults.Client
	// lastFetch throttles per tournament across all callers. In memory rather
	// than the database because a restart forgetting the throttle is harmless,
	// and a database write per read is not.
	mu        sync.Mutex
	lastFetch map[int]time.Time
}

// allowFetch reports whether a fetch of this tournament may happen now, and
// claims the slot if so.
func (c *chessResultsDeps) allowFetch(id int) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if time.Since(c.lastFetch[id]) < externalRefreshFloor {
		return false
	}
	c.lastFetch[id] = time.Now()
	return true
}

/* ---- views ---- */

type externalStanding struct {
	Rank       int     `json:"rank"`
	Name       string  `json:"name"`
	FideID     string  `json:"fideId,omitempty"`
	Federation string  `json:"federation,omitempty"`
	Rating     int     `json:"rating,omitempty"`
	Points     float64 `json:"points"`
	Club       string  `json:"club,omitempty"`
	// Set when this row is one of the academy's own students — the reason the
	// feature exists.
	StudentID   string `json:"studentId,omitempty"`
	StudentName string `json:"studentName,omitempty"`
}

type externalTournament struct {
	ID             string `json:"externalTournamentId"`
	ChessResultsID int    `json:"chessResultsId"`
	Name           string `json:"name"`
	Stage          string `json:"stage,omitempty"`
	// URL links to the source. The site is the authority; this page is the
	// academy's view of it, and saying so is part of being a polite reader.
	URL       string `json:"url"`
	FetchedAt string `json:"fetchedAt,omitempty"`
	Players   int    `json:"players"`
	// OurPlayers is what a coach scans for first.
	OurPlayers int `json:"academyPlayers"`
}

func chessResultsURL(id int) string {
	return fmt.Sprintf("https://chess-results.com/tnr%d.aspx?lan=1", id)
}

/* ---- storing ---- */

// storeExternal writes one fetched tournament and matches rows to students.
//
// Matching is FIDE ID first — the identity that survives every spelling — and
// exact normalised name second. Rows that match nobody are stored anyway: the
// table a parent sees must be the table the arbiter published, not just the
// rows we recognised.
func (c *chessResultsDeps) storeExternal(extID string, t *chessresults.Tournament) error {
	type studentKey struct{ id, name string }
	byFide := map[string]studentKey{}
	byName := map[string]studentKey{}
	rows, err := c.db.Query(`SELECT student_id, name, COALESCE(fide_id,'') FROM student`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id, name, fide string
		if err := rows.Scan(&id, &name, &fide); err != nil {
			rows.Close()
			return err
		}
		if fide != "" && fide != "0" {
			byFide[fide] = studentKey{id, name}
		}
		byName[chessresults.NormalizeName(name)] = studentKey{id, name}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	tx, err := c.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`UPDATE external_tournament SET name = ?, stage = ?, fetched_at = datetime('now')
	                      WHERE external_tournament_id = ?`, t.Name, t.Stage, extID); err != nil {
		return err
	}
	// Replaced wholesale: the source table is the truth and rows have no
	// history of their own. Half a tournament from two different fetches would
	// be worse than either fetch alone.
	if _, err := tx.Exec(`DELETE FROM external_standing WHERE external_tournament_id = ?`, extID); err != nil {
		return err
	}
	for i, r := range t.Rows {
		var studentID any
		if s, ok := byFide[r.FideID]; ok && r.FideID != "" {
			studentID = s.id
		} else if s, ok := byName[chessresults.NormalizeName(r.Name)]; ok {
			studentID = s.id
		}
		if _, err := tx.Exec(`
			INSERT INTO external_standing (external_tournament_id, position, rank, name, fide_id,
			                               federation, rating, points, club, student_id)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			extID, i+1, r.Rank, r.Name, r.FideID, r.Federation, r.Rating, r.Points, r.Club, studentID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// refreshExternal fetches and stores, respecting the politeness floor.
func (c *chessResultsDeps) refreshExternal(extID string, crID int) error {
	if !c.allowFetch(crID) {
		return errExternalThrottled
	}
	t, err := c.client.Fetch(crID)
	if err != nil {
		return err
	}
	if err := c.storeExternal(extID, t); err != nil {
		return err
	}
	// Best-effort: the standings are already stored, and a torn round page
	// must not make the whole refresh look failed.
	if err := c.syncRounds(extID, t); err != nil {
		log.Printf("chessresults: rounds for %d: %v", crID, err)
	}
	return nil
}

var errExternalThrottled = errors.New("chessresults: refreshed too recently")

// roundsFetchBudget caps the round pages one refresh may pull. A played round
// is fetched once and kept forever, so a live event costs one or two pages per
// refresh; the budget only bites when a long-finished event is first linked,
// where the history simply arrives over two refreshes instead of one.
const roundsFetchBudget = 5

// syncRounds brings the stored rounds up to what the ranking heading says has
// been played, and probes for the next round's pairings — the page a parent
// wants between the arbiter's pairing upload and the results upload.
func (c *chessResultsDeps) syncRounds(extID string, t *chessresults.Tournament) error {
	played := chessresults.PlayedRounds(t.Stage)
	final := chessresults.FinalStage(t.Stage)

	// Which rounds are already stored as played. Ids first, rows closed, then
	// any further queries — single-connection sqlite deadlocks otherwise.
	have := map[int]bool{}
	rows, err := c.db.Query(`SELECT round_no FROM external_round
	                         WHERE external_tournament_id = ? AND played = 1`, extID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var n int
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return err
		}
		have[n] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	budget := roundsFetchBudget
	for k := 1; k <= played && budget > 0; k++ {
		if have[k] {
			// A counted round is immutable: fetched once, never again.
			continue
		}
		budget--
		r, err := c.client.FetchRound(t.ID, k)
		if errors.Is(err, chessresults.ErrNoSuchRound) {
			// A hole in the site's own pages; the rounds around it still count.
			continue
		}
		if err != nil {
			return err
		}
		if err := c.storeRound(extID, r, true); err != nil {
			return err
		}
	}

	if final {
		// The event is over: a stored "next round" that never happened would
		// hang a pending round under a final table forever.
		if _, err := c.db.Exec(`DELETE FROM external_pairing
		                        WHERE external_tournament_id = ? AND round_no > ?`, extID, played); err != nil {
			return err
		}
		_, err := c.db.Exec(`DELETE FROM external_round
		                     WHERE external_tournament_id = ? AND round_no > ?`, extID, played)
		return err
	}
	if budget <= 0 {
		return nil
	}

	// The next round: published before it is played, and replaced wholesale on
	// every refresh because pairings can be corrected until play starts.
	next := played + 1
	r, err := c.client.FetchRound(t.ID, next)
	if errors.Is(err, chessresults.ErrNoSuchRound) {
		return nil
	}
	if err != nil {
		return err
	}
	return c.storeRound(extID, r, false)
}

// storeRound writes one round, matching seats to students by exact normalised
// name — the pairing page has no FIDE column, so name is all there is.
func (c *chessResultsDeps) storeRound(extID string, r *chessresults.Round, played bool) error {
	byName := map[string]string{}
	rows, err := c.db.Query(`SELECT student_id, name FROM student`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			rows.Close()
			return err
		}
		byName[chessresults.NormalizeName(name)] = id
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	tx, err := c.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	playedInt := 0
	if played {
		playedInt = 1
	}
	if _, err := tx.Exec(`
		INSERT INTO external_round (external_tournament_id, round_no, played, round_date, fetched_at)
		VALUES (?, ?, ?, ?, datetime('now'))
		ON CONFLICT(external_tournament_id, round_no)
		DO UPDATE SET played = excluded.played, round_date = excluded.round_date,
		              fetched_at = excluded.fetched_at`,
		extID, r.Number, playedInt, r.Date); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM external_pairing
	                      WHERE external_tournament_id = ? AND round_no = ?`, extID, r.Number); err != nil {
		return err
	}
	for _, pr := range r.Pairings {
		var white, black any
		if id, ok := byName[chessresults.NormalizeName(pr.WhiteName)]; ok {
			white = id
		}
		if id, ok := byName[chessresults.NormalizeName(pr.BlackName)]; ok {
			black = id
		}
		if _, err := tx.Exec(`
			INSERT INTO external_pairing (external_tournament_id, round_no, board,
			    white_name, white_rating, black_name, black_rating, result,
			    white_student_id, black_student_id)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			extID, r.Number, pr.Board, pr.WhiteName, pr.WhiteRating,
			pr.BlackName, pr.BlackRating, pr.Result, white, black); err != nil {
			return err
		}
	}
	return tx.Commit()
}

/* ---- endpoints ---- */

// handleTrackExternal starts tracking a tournament. Staff only: each track
// costs outbound requests to a third party, and deciding what the academy
// follows is a front-desk call.
func handleTrackExternal(c *chessResultsDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := requireIdentity(c.db, w, r)
		if id == nil {
			return
		}
		if !isStaff(id.Role) {
			httpx.Error(w, http.StatusForbidden, "not allowed", nil)
			return
		}
		var in struct {
			URL string `json:"url"`
		}
		if err := httpx.Decode(r, &in); err != nil {
			httpx.Error(w, http.StatusBadRequest, "a chess-results.com link is required", err)
			return
		}
		crID, err := chessresults.ParseRef(in.URL)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, "that does not look like a chess-results.com tournament link", nil)
			return
		}

		// Already tracked is answered with the existing record rather than an
		// error: two members of staff pasting the same link both meant "make
		// sure we follow this", and both got their wish.
		var existing string
		err = c.db.QueryRow(`SELECT external_tournament_id FROM external_tournament
		                     WHERE chess_results_id = ?`, crID).Scan(&existing)
		if err == nil {
			view, verr := loadExternal(c.db, existing)
			if verr != nil {
				httpx.Error(w, http.StatusInternalServerError, "could not load", verr)
				return
			}
			httpx.JSON(w, http.StatusOK, view)
			return
		}
		if !errors.Is(err, sql.ErrNoRows) {
			httpx.Error(w, http.StatusInternalServerError, "could not check", err)
			return
		}

		if !c.allowFetch(crID) {
			httpx.Error(w, http.StatusTooManyRequests, "that tournament was fetched moments ago, try again shortly", nil)
			return
		}
		t, err := c.client.Fetch(crID)
		if err != nil {
			log.Printf("chessresults: fetching %d: %v", crID, err)
			httpx.Error(w, http.StatusBadGateway, "chess-results.com could not be read — check the link, or try again shortly", err)
			return
		}

		extID := newID("ext")
		if _, err := c.db.Exec(`INSERT INTO external_tournament (external_tournament_id, chess_results_id, name, created_by)
		                        VALUES (?, ?, ?, ?)`, extID, crID, t.Name, id.UserAccountID); err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not save", err)
			return
		}
		if err := c.storeExternal(extID, t); err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not save the standings", err)
			return
		}
		if err := c.syncRounds(extID, t); err != nil {
			log.Printf("chessresults: rounds for %d: %v", crID, err)
		}
		view, err := loadExternal(c.db, extID)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "saved but could not reload", err)
			return
		}
		httpx.JSON(w, http.StatusCreated, view)
	}
}

func loadExternal(d *sql.DB, extID string) (*externalTournament, error) {
	var v externalTournament
	var fetched sql.NullString
	err := d.QueryRow(`
		SELECT t.external_tournament_id, t.chess_results_id, t.name, t.stage, COALESCE(t.fetched_at,''),
		       (SELECT COUNT(*) FROM external_standing s WHERE s.external_tournament_id = t.external_tournament_id),
		       (SELECT COUNT(*) FROM external_standing s WHERE s.external_tournament_id = t.external_tournament_id
		         AND s.student_id IS NOT NULL)
		FROM external_tournament t WHERE t.external_tournament_id = ?`, extID).
		Scan(&v.ID, &v.ChessResultsID, &v.Name, &v.Stage, &fetched, &v.Players, &v.OurPlayers)
	if err != nil {
		return nil, err
	}
	if fetched.Valid && fetched.String != "" {
		v.FetchedAt = sqliteISO(fetched.String)
	}
	v.URL = chessResultsURL(v.ChessResultsID)
	return &v, nil
}

// handleListExternal lists tracked tournaments. Any signed-in user: the data
// is already public on chess-results.com, and a parent following their child's
// event is the audience.
func handleListExternal(c *chessResultsDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if requireIdentity(c.db, w, r) == nil {
			return
		}
		// Ids first, rows closed, *then* the per-tournament loads. The local
		// database runs with a single connection (modernc sqlite is
		// single-writer), so a query issued while these rows were still open
		// would wait for the connection the rows are holding — a deadlock
		// that took the whole server down with it, discovered the first time
		// the console opened this list against a live backend.
		ids := []string{}
		rows, err := c.db.Query(`SELECT external_tournament_id FROM external_tournament
		                         ORDER BY created_at DESC, external_tournament_id DESC`)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not load", err)
			return
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				httpx.Error(w, http.StatusInternalServerError, "could not load", err)
				return
			}
			ids = append(ids, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not load", err)
			return
		}

		out := []externalTournament{}
		for _, id := range ids {
			v, err := loadExternal(c.db, id)
			if err != nil {
				httpx.Error(w, http.StatusInternalServerError, "could not load", err)
				return
			}
			out = append(out, *v)
		}
		httpx.JSON(w, http.StatusOK, out)
	}
}

// handleGetExternal returns one tournament with its standings, refreshing the
// copy first when it is stale and the event might still be moving.
func handleGetExternal(c *chessResultsDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if requireIdentity(c.db, w, r) == nil {
			return
		}
		extID := r.PathValue("id")
		view, err := loadExternal(c.db, extID)
		if errors.Is(err, sql.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, "not found", nil)
			return
		}
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not load", err)
			return
		}

		// "Final Ranking" never changes again; everything else might. The
		// refresh is best-effort — a read must not fail because the site is
		// down, it just serves the last good copy.
		if !strings.HasPrefix(strings.ToLower(view.Stage), "final") && staleExternal(view.FetchedAt) {
			if err := c.refreshExternal(extID, view.ChessResultsID); err != nil &&
				!errors.Is(err, errExternalThrottled) {
				log.Printf("chessresults: background refresh %d: %v", view.ChessResultsID, err)
			}
			if fresh, err := loadExternal(c.db, extID); err == nil {
				view = fresh
			}
		}

		standings, err := loadExternalStandings(c.db, extID)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not load standings", err)
			return
		}
		rounds, err := loadExternalRounds(c.db, extID)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not load rounds", err)
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"tournament": view, "standings": standings, "rounds": rounds})
	}
}

func staleExternal(fetchedISO string) bool {
	if fetchedISO == "" {
		return true
	}
	t, err := time.Parse(time.RFC3339, fetchedISO)
	if err != nil {
		return true
	}
	return time.Since(t) > externalSyncInterval
}

func loadExternalStandings(d *sql.DB, extID string) ([]externalStanding, error) {
	rows, err := d.Query(`
		SELECT s.rank, s.name, s.fide_id, s.federation, s.rating, s.points, s.club,
		       COALESCE(s.student_id,''), COALESCE(st.name,'')
		FROM external_standing s
		LEFT JOIN student st ON st.student_id = s.student_id
		WHERE s.external_tournament_id = ?
		ORDER BY s.position`, extID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []externalStanding{}
	for rows.Next() {
		var s externalStanding
		if err := rows.Scan(&s.Rank, &s.Name, &s.FideID, &s.Federation, &s.Rating,
			&s.Points, &s.Club, &s.StudentID, &s.StudentName); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// handleRefreshExternal re-reads the source now. Staff only, and throttled:
// this is the button that costs a third party requests.
func handleRefreshExternal(c *chessResultsDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := requireIdentity(c.db, w, r)
		if id == nil {
			return
		}
		if !isStaff(id.Role) {
			httpx.Error(w, http.StatusForbidden, "not allowed", nil)
			return
		}
		extID := r.PathValue("id")
		view, err := loadExternal(c.db, extID)
		if errors.Is(err, sql.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, "not found", nil)
			return
		}
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not load", err)
			return
		}
		if err := c.refreshExternal(extID, view.ChessResultsID); err != nil {
			if errors.Is(err, errExternalThrottled) {
				httpx.Error(w, http.StatusTooManyRequests, "refreshed moments ago — chess-results.com asks readers to go gently", nil)
				return
			}
			log.Printf("chessresults: refresh %d: %v", view.ChessResultsID, err)
			httpx.Error(w, http.StatusBadGateway, "chess-results.com could not be read, the previous standings still stand", err)
			return
		}
		fresh, err := loadExternal(c.db, extID)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "refreshed but could not reload", err)
			return
		}
		httpx.JSON(w, http.StatusOK, fresh)
	}
}

// handleUntrackExternal stops following a tournament. Staff only. The copy is
// deleted outright — the source of truth is still on chess-results.com, so
// nothing of record is lost.
func handleUntrackExternal(c *chessResultsDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := requireIdentity(c.db, w, r)
		if id == nil {
			return
		}
		if !isStaff(id.Role) {
			httpx.Error(w, http.StatusForbidden, "not allowed", nil)
			return
		}
		extID := r.PathValue("id")
		tx, err := c.db.Begin()
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not delete", err)
			return
		}
		defer tx.Rollback()
		if _, err := tx.Exec(`DELETE FROM external_standing WHERE external_tournament_id = ?`, extID); err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not delete", err)
			return
		}
		if _, err := tx.Exec(`DELETE FROM external_pairing WHERE external_tournament_id = ?`, extID); err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not delete", err)
			return
		}
		if _, err := tx.Exec(`DELETE FROM external_round WHERE external_tournament_id = ?`, extID); err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not delete", err)
			return
		}
		res, err := tx.Exec(`DELETE FROM external_tournament WHERE external_tournament_id = ?`, extID)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not delete", err)
			return
		}
		if n, _ := res.RowsAffected(); n == 0 {
			httpx.Error(w, http.StatusNotFound, "not found", nil)
			return
		}
		if err := tx.Commit(); err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not delete", err)
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"deleted": true})
	}
}

/* ---- mount ---- */

func mountChessResults(mux *http.ServeMux, d *sql.DB) *chessResultsDeps {
	deps := &chessResultsDeps{db: d, client: chessresults.New(), lastFetch: map[int]time.Time{}}
	if base := strings.TrimSpace(os.Getenv("CHESS_RESULTS_API_BASE")); base != "" {
		// A seam for tests and for a deployment behind an egress proxy.
		deps.client.BaseURL = base
	}
	const p = "/api/v1/external-tournaments"
	// Track carries a budget on top of the per-tournament floor: it is the one
	// endpoint where a caller-chosen number becomes an outbound request.
	mux.HandleFunc("POST "+p, httpx.RateLimit(10, handleTrackExternal(deps)))
	mux.HandleFunc("GET "+p, handleListExternal(deps))
	mux.HandleFunc("GET "+p+"/{id}", handleGetExternal(deps))
	mux.HandleFunc("POST "+p+"/{id}/refresh", httpx.RateLimit(10, handleRefreshExternal(deps)))
	mux.HandleFunc("DELETE "+p+"/{id}", handleUntrackExternal(deps))

	// Tying one of the academy's own tournaments to an event on that site.
	// Mounted here so both share one client and one per-tournament fetch
	// throttle — two throttles would be no throttle at all.
	const t = "/api/v1/tournaments"
	mux.HandleFunc("GET "+t+"/{id}/chess-results", handleGetChessResultsLink(deps))
	mux.HandleFunc("POST "+t+"/{id}/chess-results", httpx.RateLimit(10, handleLinkChessResults(deps)))
	mux.HandleFunc("DELETE "+t+"/{id}/chess-results", handleUnlinkChessResults(deps))
	mux.HandleFunc("POST "+t+"/{id}/chess-results/refresh", httpx.RateLimit(10, handleRefreshTournamentResults(deps)))
	return deps
}
