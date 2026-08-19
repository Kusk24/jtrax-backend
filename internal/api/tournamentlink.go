// Linking one of the academy's tournaments to the chess-results.com event it is
// published as, so its public page follows the arbiter instead of the console.
//
// The link reuses the tracking machinery in chessresults.go rather than growing
// a second copy of it: linking a tournament also starts following that event in
// the external list, which is what staff mean by it. Unlinking only breaks the
// tournament's tie — the event stays tracked, because somebody put it there.
package api

import (
	"database/sql"
	"errors"
	"log"
	"net/http"

	"github.com/Kusk24/jtrax-backend/internal/chessresults"
	"github.com/Kusk24/jtrax-backend/internal/httpx"
)

// linkedResults is a tournament's standings as read from chess-results.com.
//
// Carries where it came from and when, because a page served from a cache of
// somebody else's site has to say so — a parent reading a stale table deserves
// to know it is stale, and the link out is how they check.
type linkedResults struct {
	Source     string             `json:"source"` // always "chess-results"
	URL        string             `json:"url"`
	Stage      string             `json:"stage,omitempty"`
	FetchedAt  string             `json:"fetchedAt,omitempty"`
	Standings  []externalStanding `json:"standings"`
	ChessResID int                `json:"chessResultsId"`
}

// linkedResultsFor returns the external standings for a tournament, or nil when
// it has no chess-results event — in which case the caller falls back to the
// standings the console keeps itself.
func linkedResultsFor(d *sql.DB, tournamentID string) (*linkedResults, error) {
	var crID sql.NullInt64
	if err := d.QueryRow(`SELECT chess_results_id FROM tournament WHERE tournament_id = ?`,
		tournamentID).Scan(&crID); err != nil {
		return nil, err
	}
	if !crID.Valid {
		return nil, nil
	}
	var extID, stage, fetched string
	err := d.QueryRow(`SELECT external_tournament_id, stage, COALESCE(fetched_at,'')
	                   FROM external_tournament WHERE chess_results_id = ?`,
		crID.Int64).Scan(&extID, &stage, &fetched)
	if errors.Is(err, sql.ErrNoRows) {
		// Linked to an event nobody is tracking any more. Not an error: the
		// tournament falls back to its own standings rather than 500ing.
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	rows, err := loadExternalStandings(d, extID)
	if err != nil {
		return nil, err
	}
	return &linkedResults{
		Source: "chess-results", URL: chessResultsURL(int(crID.Int64)),
		Stage: stage, FetchedAt: fetched, Standings: rows,
		ChessResID: int(crID.Int64),
	}, nil
}

// publicExternalStandings strips a linked table down to what may be shown
// without a session.
//
// The two fields dropped are the ones this product added: the student id and
// the academy's own name for that child. The arbiter's published name stays,
// because it is already public on chess-results.com — but which of those rows
// is one of our pupils is the academy's knowledge, not the public's.
func publicExternalStandings(rows []externalStanding) []map[string]any {
	out := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		out = append(out, map[string]any{
			"rank": r.Rank, "name": r.Name, "points": r.Points,
			"federation": r.Federation, "rating": r.Rating, "club": r.Club,
		})
	}
	return out
}

// handleLinkChessResults points a tournament at a chess-results event.
//
// Tracking the event and linking it are one action deliberately: a member of
// staff pasting the link onto a tournament means "this is that", and having to
// also add it to a separate list would be a way to end up with a tournament
// linked to an event whose standings nobody ever fetches.
func handleLinkChessResults(c *chessResultsDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := requireStaff(c.db, w, r)
		if id == nil {
			return
		}
		tournamentID := r.PathValue("id")
		var exists int
		if err := c.db.QueryRow(`SELECT COUNT(*) FROM tournament WHERE tournament_id = ?`,
			tournamentID).Scan(&exists); err != nil || exists == 0 {
			httpx.Error(w, http.StatusNotFound, "not found", err)
			return
		}

		var in struct {
			URL string `json:"url"`
		}
		if err := httpx.Decode(r, &in); err != nil {
			httpx.Error(w, http.StatusBadRequest, "a chess-results.com link is required", err)
			return
		}
		// ParseRef also refuses any host that is not chess-results.com, which is
		// what stops this from being a way to make the server fetch a URL of the
		// caller's choosing.
		crID, err := chessresults.ParseRef(in.URL)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest,
				"that does not look like a chess-results.com tournament link", nil)
			return
		}

		// Track it if it is new. Already tracked is the ordinary case once a
		// second tournament from the same series is linked.
		var extID string
		err = c.db.QueryRow(`SELECT external_tournament_id FROM external_tournament
		                     WHERE chess_results_id = ?`, crID).Scan(&extID)
		if errors.Is(err, sql.ErrNoRows) {
			if !c.allowFetch(crID) {
				httpx.Error(w, http.StatusTooManyRequests,
					"that tournament was fetched moments ago, try again shortly", nil)
				return
			}
			t, ferr := c.client.Fetch(crID)
			if ferr != nil {
				log.Printf("chessresults: fetching %d for tournament %s: %v", crID, tournamentID, ferr)
				httpx.Error(w, http.StatusBadGateway,
					"chess-results.com could not be read — check the link, or try again shortly", ferr)
				return
			}
			extID = newID("ext")
			if _, err := c.db.Exec(`INSERT INTO external_tournament
			                        (external_tournament_id, chess_results_id, name, created_by)
			                        VALUES (?, ?, ?, ?)`,
				extID, crID, t.Name, id.UserAccountID); err != nil {
				httpx.Error(w, http.StatusInternalServerError, "could not save", err)
				return
			}
			if err := c.storeExternal(extID, t); err != nil {
				httpx.Error(w, http.StatusInternalServerError, "could not save the standings", err)
				return
			}
		} else if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not check", err)
			return
		}

		if _, err := c.db.Exec(`UPDATE tournament SET chess_results_id = ? WHERE tournament_id = ?`,
			crID, tournamentID); err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not link", err)
			return
		}
		out, err := linkedResultsFor(c.db, tournamentID)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "linked but could not reload", err)
			return
		}
		httpx.JSON(w, http.StatusOK, out)
	}
}

// handleUnlinkChessResults breaks the tie and gives the tournament its own
// standings back. The external event stays tracked.
func handleUnlinkChessResults(c *chessResultsDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if requireStaff(c.db, w, r) == nil {
			return
		}
		if _, err := c.db.Exec(`UPDATE tournament SET chess_results_id = NULL WHERE tournament_id = ?`,
			r.PathValue("id")); err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not unlink", err)
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"linked": false})
	}
}

// handleRefreshTournamentResults re-reads the linked event now.
//
// Staff-only and throttled by the same per-tournament floor as every other
// fetch: the button exists so the desk can pull a round in without waiting for
// the timer, not so it can be held down.
func handleRefreshTournamentResults(c *chessResultsDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if requireStaff(c.db, w, r) == nil {
			return
		}
		tournamentID := r.PathValue("id")
		var crID sql.NullInt64
		if err := c.db.QueryRow(`SELECT chess_results_id FROM tournament WHERE tournament_id = ?`,
			tournamentID).Scan(&crID); err != nil {
			httpx.Error(w, http.StatusNotFound, "not found", err)
			return
		}
		if !crID.Valid {
			httpx.Error(w, http.StatusBadRequest, "this tournament is not linked to chess-results.com", nil)
			return
		}
		var extID string
		if err := c.db.QueryRow(`SELECT external_tournament_id FROM external_tournament
		                         WHERE chess_results_id = ?`, crID.Int64).Scan(&extID); err != nil {
			httpx.Error(w, http.StatusNotFound, "that event is no longer tracked", err)
			return
		}
		if !c.allowFetch(int(crID.Int64)) {
			httpx.Error(w, http.StatusTooManyRequests,
				"that tournament was fetched moments ago, try again shortly", nil)
			return
		}
		if err := c.refreshExternal(extID, int(crID.Int64)); err != nil {
			log.Printf("chessresults: refreshing %d: %v", crID.Int64, err)
			httpx.Error(w, http.StatusBadGateway, "chess-results.com could not be read just now", err)
			return
		}
		out, err := linkedResultsFor(c.db, tournamentID)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "refreshed but could not reload", err)
			return
		}
		httpx.JSON(w, http.StatusOK, out)
	}
}
