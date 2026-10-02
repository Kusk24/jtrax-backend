// Connecting a tournament to its chess-results.com event with one link.
//
// The admin pastes a link to any one section. The details page of that
// section lists every section of the event ("Tournament selection"), so the
// server finds them all, stores them as the tournament's *results categories*
// (migration 0051), and fetches each one's standings.
//
// These are deliberately not the registration categories. The office sets up
// U8, U10, U12 and U14 to take entries; the arbiter may well run a U16 as
// well, or merge two groups into "U14 + G14". Results follow the arbiter's
// list, and neither list is copied into the other.
package api

import (
	"database/sql"
	"errors"
	"log"
	"net/http"
	"strconv"

	"github.com/Kusk24/jtrax-backend/internal/chessresults"
	"github.com/Kusk24/jtrax-backend/internal/httpx"
)

// resultSection is one chess-results category of a connected tournament, with
// enough about its stored copy to draw a tab and a status line.
type resultSection struct {
	ChessResultsID int    `json:"chessResultsId"`
	Name           string `json:"name"`
	Position       int    `json:"position"`
	URL            string `json:"url"`
	// Empty until the section's standings have been read at least once.
	Stage     string `json:"stage,omitempty"`
	FetchedAt string `json:"fetchedAt,omitempty"`
	Players   int    `json:"players"`
	// How many rows were recognised as the academy's own students.
	AcademyPlayers int `json:"academyPlayers"`
	// Whether a stored copy exists yet. A section whose first read failed is
	// still listed, and its tab offers a refresh.
	Tracked bool `json:"tracked"`
}

type resultSectionsView struct {
	Connected bool            `json:"connected"`
	EventName string          `json:"eventName,omitempty"`
	Rounds    int             `json:"rounds,omitempty"`
	Sections  []resultSection `json:"sections"`
}

// loadResultSections reads a tournament's results categories, in order.
func loadResultSections(d *sql.DB, tournamentID string) (*resultSectionsView, error) {
	out := &resultSectionsView{Sections: []resultSection{}}
	var event sql.NullString
	var rounds sql.NullInt64
	if err := d.QueryRow(`SELECT chess_results_event, chess_results_rounds FROM tournament WHERE tournament_id = ?`,
		tournamentID).Scan(&event, &rounds); err != nil {
		return nil, err
	}
	rows, err := d.Query(`
		SELECT s.chess_results_id, s.name, s.position,
		       COALESCE(e.stage,''), COALESCE(e.fetched_at,''),
		       (SELECT COUNT(*) FROM external_standing x WHERE x.external_tournament_id = e.external_tournament_id),
		       (SELECT COUNT(*) FROM external_standing x WHERE x.external_tournament_id = e.external_tournament_id
		                                                   AND x.student_id IS NOT NULL),
		       e.external_tournament_id IS NOT NULL
		FROM tournament_result_section s
		LEFT JOIN external_tournament e ON e.chess_results_id = s.chess_results_id
		WHERE s.tournament_id = ?
		ORDER BY s.position, s.chess_results_id`, tournamentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var s resultSection
		if err := rows.Scan(&s.ChessResultsID, &s.Name, &s.Position, &s.Stage, &s.FetchedAt,
			&s.Players, &s.AcademyPlayers, &s.Tracked); err != nil {
			return nil, err
		}
		s.URL = chessResultsURL(s.ChessResultsID)
		out.Sections = append(out.Sections, s)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out.Connected = len(out.Sections) > 0
	if out.Connected {
		out.EventName = event.String
		out.Rounds = int(rounds.Int64)
	}
	return out, nil
}

// ensureTracked makes sure a stored copy of one section exists, fetching it the
// first time. Unlike trackEvent it reports errors instead of writing them, so
// one section failing does not abandon the rest.
func (c *chessResultsDeps) ensureTracked(crID int, byUser string) error {
	var extID string
	err := c.db.QueryRow(`SELECT external_tournament_id FROM external_tournament WHERE chess_results_id = ?`,
		crID).Scan(&extID)
	if err == nil {
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if !c.allowFetch(crID) {
		return errExternalThrottled
	}
	t, err := c.client.Fetch(crID)
	if err != nil {
		return err
	}
	extID = newID("ext")
	if _, err := c.db.Exec(`INSERT INTO external_tournament (external_tournament_id, chess_results_id, name, created_by)
	                        VALUES (?, ?, ?, ?)`, extID, crID, t.Name, byUser); err != nil {
		return err
	}
	if err := c.storeExternal(extID, t); err != nil {
		return err
	}
	if err := c.syncRounds(extID, t); err != nil {
		log.Printf("chessresults: rounds for %d: %v", crID, err)
	}
	return nil
}

// eventFetchKey throttles reads of an event's details page separately from its
// standings, so connecting does not use up the slot the first standings read
// needs a moment later. Negative so it cannot collide with a tnr number.
func eventFetchKey(crID int) int { return -crID }

// handleConnectResults connects a tournament from one pasted link.
func handleConnectResults(c *chessResultsDeps) http.HandlerFunc {
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
		// ParseRef refuses every host but chess-results.com: this is where a
		// caller's text becomes an outbound request.
		crID, err := chessresults.ParseRef(in.URL)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, "that does not look like a chess-results.com tournament link", nil)
			return
		}
		if !c.allowFetch(eventFetchKey(crID)) {
			httpx.Error(w, http.StatusTooManyRequests, "that event was read moments ago, try again shortly", nil)
			return
		}
		ev, err := c.client.FetchEvent(crID)
		if err != nil {
			log.Printf("chessresults: event %d: %v", crID, err)
			httpx.Error(w, http.StatusBadGateway, "chess-results.com could not be read — check the link, or try again shortly", err)
			return
		}

		// The arbiter's list replaces whatever was connected before, in one go.
		err = inTx(c.db, func(tx *sql.Tx) error {
			if _, err := tx.Exec(`DELETE FROM tournament_result_section WHERE tournament_id = ?`, tournamentID); err != nil {
				return err
			}
			for i, s := range ev.Sections {
				if _, err := tx.Exec(`INSERT OR IGNORE INTO tournament_result_section
				                      (tournament_id, chess_results_id, name, position) VALUES (?, ?, ?, ?)`,
					tournamentID, s.ID, s.Name, i); err != nil {
					return err
				}
			}
			var rounds any
			if ev.Rounds > 0 {
				rounds = ev.Rounds
			}
			_, err := tx.Exec(`UPDATE tournament SET chess_results_event = ?, chess_results_rounds = ? WHERE tournament_id = ?`,
				ev.Name, rounds, tournamentID)
			return err
		})
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not save the categories", err)
			return
		}

		// Each section's standings, best-effort: a section that cannot be read
		// right now is still listed, and its tab offers a refresh.
		for _, s := range ev.Sections {
			if err := c.ensureTracked(s.ID, id.UserAccountID); err != nil {
				log.Printf("chessresults: section %d of %d: %v", s.ID, crID, err)
			}
		}

		out, err := loadResultSections(c.db, tournamentID)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "connected but could not reload", err)
			return
		}
		httpx.JSON(w, http.StatusOK, out)
	}
}

func handleListResultSections(c *chessResultsDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if requireStaff(c.db, w, r) == nil {
			return
		}
		out, err := loadResultSections(c.db, r.PathValue("id"))
		if errors.Is(err, sql.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, "not found", nil)
			return
		}
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not load", err)
			return
		}
		httpx.JSON(w, http.StatusOK, out)
	}
}

// sectionOf reads the section id from the path and checks it belongs to the
// tournament, so one tournament's route cannot read another's event.
func sectionOf(c *chessResultsDeps, w http.ResponseWriter, r *http.Request) (int, bool) {
	crID, err := strconv.Atoi(r.PathValue("crId"))
	if err != nil || crID <= 0 {
		httpx.Error(w, http.StatusNotFound, "not found", nil)
		return 0, false
	}
	var n int
	if err := c.db.QueryRow(`SELECT COUNT(*) FROM tournament_result_section
	                         WHERE tournament_id = ? AND chess_results_id = ?`,
		r.PathValue("id"), crID).Scan(&n); err != nil || n == 0 {
		httpx.Error(w, http.StatusNotFound, "not found", err)
		return 0, false
	}
	return crID, true
}

// handleGetResultSection serves one results category's stored standings and
// rounds. Read-only: opening a tab never costs chess-results a request.
func handleGetResultSection(c *chessResultsDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if requireStaff(c.db, w, r) == nil {
			return
		}
		crID, ok := sectionOf(c, w, r)
		if !ok {
			return
		}
		out, err := linkedResultsForID(c.db, sql.NullInt64{Int64: int64(crID), Valid: true})
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not load", err)
			return
		}
		if out == nil {
			httpx.JSON(w, http.StatusOK, map[string]any{"linked": false, "chessResultsId": crID, "url": chessResultsURL(crID)})
			return
		}
		httpx.JSON(w, http.StatusOK, out)
	}
}

// handleRefreshResultSection re-reads one results category now — the one on
// screen, not the tournament's old single link.
func handleRefreshResultSection(c *chessResultsDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := requireStaff(c.db, w, r)
		if id == nil {
			return
		}
		crID, ok := sectionOf(c, w, r)
		if !ok {
			return
		}
		var extID string
		err := c.db.QueryRow(`SELECT external_tournament_id FROM external_tournament WHERE chess_results_id = ?`,
			crID).Scan(&extID)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			// Its first read failed at connect time; this is that read.
			err = c.ensureTracked(crID, id.UserAccountID)
		case err == nil:
			err = c.refreshExternal(extID, crID)
		}
		if errors.Is(err, errExternalThrottled) {
			httpx.Error(w, http.StatusTooManyRequests, "that category was read moments ago, try again shortly", nil)
			return
		}
		if err != nil {
			log.Printf("chessresults: refreshing section %d: %v", crID, err)
			httpx.Error(w, http.StatusBadGateway, "chess-results.com could not be read just now", err)
			return
		}
		out, err := linkedResultsForID(c.db, sql.NullInt64{Int64: int64(crID), Valid: true})
		if err != nil || out == nil {
			httpx.Error(w, http.StatusInternalServerError, "refreshed but could not reload", err)
			return
		}
		httpx.JSON(w, http.StatusOK, out)
	}
}

// handleDisconnectResults forgets the tournament's results categories. The
// stored copies stay tracked, and the registration categories are untouched.
func handleDisconnectResults(c *chessResultsDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if requireStaff(c.db, w, r) == nil {
			return
		}
		tournamentID := r.PathValue("id")
		err := inTx(c.db, func(tx *sql.Tx) error {
			if _, err := tx.Exec(`DELETE FROM tournament_result_section WHERE tournament_id = ?`, tournamentID); err != nil {
				return err
			}
			_, err := tx.Exec(`UPDATE tournament SET chess_results_event = NULL, chess_results_rounds = NULL
			                   WHERE tournament_id = ?`, tournamentID)
			return err
		})
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not disconnect", err)
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"connected": false, "sections": []resultSection{}})
	}
}

func mountResultSections(mux *http.ServeMux, c *chessResultsDeps) {
	const p = "/api/v1/tournaments/{id}/results-sections"
	mux.HandleFunc("GET "+p, handleListResultSections(c))
	// Connecting reads every section, so it carries the same outbound budget
	// as tracking an event.
	mux.HandleFunc("POST "+p, httpx.RateLimit(10, handleConnectResults(c)))
	mux.HandleFunc("DELETE "+p, handleDisconnectResults(c))
	mux.HandleFunc("GET "+p+"/sections/{crId}", handleGetResultSection(c))
	mux.HandleFunc("POST "+p+"/sections/{crId}/refresh", httpx.RateLimit(10, handleRefreshResultSection(c)))
}
