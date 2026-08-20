// Tournament rounds, pairings and the live standings they produce.
//
// The ER model has tournaments, categories and registrations but nothing for
// what happened, which is why the console showed every player on a score of
// "—". These endpoints are that missing half.
//
// One of them is **unauthenticated**: a tournament's standings can be opened by
// anybody once an organiser publishes them, because a results table that only
// signed-in parents can see is not a results table. That endpoint is opt-in per
// event, rate-limited, and returns strictly less than the staff view.
package api

import (
	"database/sql"
	"errors"
	"log"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/Kusk24/jtrax-backend/internal/chessresults"
	"github.com/Kusk24/jtrax-backend/internal/httpx"
	"github.com/Kusk24/jtrax-backend/internal/standings"
)

// resultCodes are the results a board may be given. Kept next to the CHECK
// constraint it mirrors; a value not in this list is a 400 rather than a
// constraint violation surfacing as a 500.
var resultCodes = []string{
	standings.Pending, standings.WhiteWin, standings.BlackWin,
	standings.Draw, standings.WhiteFF, standings.BlackFF, standings.Bye,
}

type pairingView struct {
	ID       string `json:"pairingId"`
	Board    int    `json:"board"`
	Round    int    `json:"round"`
	White    string `json:"whiteRegistrationId"`
	WhiteN   string `json:"white"`
	Black    string `json:"blackRegistrationId,omitempty"`
	BlackN   string `json:"black,omitempty"`
	Result   string `json:"result"`
	Recorded string `json:"recordedAt,omitempty"`
}

type roundView struct {
	ID       string        `json:"roundId"`
	Round    int           `json:"round"`
	Status   string        `json:"status"`
	Pairings []pairingView `json:"pairings"`
}

type standingView struct {
	standings.Row
	Name     string  `json:"name"`
	Category string  `json:"category,omitempty"`
	Rating   float64 `json:"rating,omitempty"`
}

// loadResults reads a tournament's rounds, pairings and computed table.
func loadResults(d *sql.DB, tournamentID string) ([]roundView, []standingView, error) {
	rows, err := d.Query(`
		SELECT r.tournament_round_id, r.round_no, r.status,
		       COALESCE(p.tournament_pairing_id,''), COALESCE(p.board_no,0),
		       COALESCE(p.white_registration_id,''), COALESCE(wr.participant_name,''),
		       COALESCE(p.black_registration_id,''), COALESCE(br.participant_name,''),
		       COALESCE(p.result,''), COALESCE(p.recorded_at,'')
		FROM tournament_round r
		LEFT JOIN tournament_pairing p ON p.tournament_round_id = r.tournament_round_id
		LEFT JOIN tournament_registration wr ON wr.tournament_registration_id = p.white_registration_id
		LEFT JOIN tournament_registration br ON br.tournament_registration_id = p.black_registration_id
		WHERE r.tournament_id = ?
		ORDER BY r.round_no, p.board_no`, tournamentID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	roundsByNo := map[int]int{}
	rounds := []roundView{}
	all := []standings.Pairing{}
	for rows.Next() {
		var roundID, status, pid, white, whiteN, black, blackN, result, recorded string
		var roundNo, board int
		if err := rows.Scan(&roundID, &roundNo, &status, &pid, &board,
			&white, &whiteN, &black, &blackN, &result, &recorded); err != nil {
			return nil, nil, err
		}
		i, ok := roundsByNo[roundNo]
		if !ok {
			rounds = append(rounds, roundView{ID: roundID, Round: roundNo, Status: status, Pairings: []pairingView{}})
			i = len(rounds) - 1
			roundsByNo[roundNo] = i
		}
		// The LEFT JOIN gives one empty row for a round with no boards yet.
		if pid == "" {
			continue
		}
		rounds[i].Pairings = append(rounds[i].Pairings, pairingView{
			ID: pid, Board: board, Round: roundNo,
			White: white, WhiteN: whiteN, Black: black, BlackN: blackN,
			Result: result, Recorded: recorded,
		})
		all = append(all, standings.Pairing{Round: roundNo, White: white, Black: black, Result: result})
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	// Everyone registered, so a player yet to be paired still appears.
	preg, err := d.Query(`SELECT tr.tournament_registration_id, tr.participant_name,
	                             COALESCE(tc.name,''), COALESCE(tr.fide_rating,0)
	                      FROM tournament_registration tr
	                      LEFT JOIN tournament_category tc
	                             ON tc.tournament_category_id = tr.tournament_category_id
	                      WHERE tr.tournament_id = ?`, tournamentID)
	if err != nil {
		return nil, nil, err
	}
	defer preg.Close()
	type meta struct {
		name, category string
		rating         float64
	}
	info := map[string]meta{}
	ids := []string{}
	for preg.Next() {
		var id string
		var m meta
		if err := preg.Scan(&id, &m.name, &m.category, &m.rating); err != nil {
			return nil, nil, err
		}
		info[id] = m
		ids = append(ids, id)
	}
	if err := preg.Err(); err != nil {
		return nil, nil, err
	}

	table := []standingView{}
	for _, row := range standings.Compute(ids, all) {
		m := info[row.RegistrationID]
		table = append(table, standingView{Row: row, Name: m.name, Category: m.category, Rating: m.rating})
	}
	return rounds, table, nil
}

func handleTournamentResults(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if requireIdentity(d, w, r) == nil {
			return
		}
		id := r.PathValue("id")
		rounds, table, err := loadResults(d, id)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not load results", err)
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"rounds": rounds, "standings": table})
	}
}

// handlePublicResults serves a published tournament to anyone.
//
// Three things make this safe to leave open: the organiser has to turn it on
// per event, it is rate-limited, and it carries only what is already pinned to
// the wall at a tournament hall — name, category, score. No contact details, no
// date of birth, no student id, nothing that ties a child to a JTrax account.
func handlePublicResults(d *sql.DB, cr *chessResultsDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		var name, status string
		var public int
		err := d.QueryRow(`SELECT name, tournament_status, results_public
		                   FROM tournament WHERE tournament_id = ?`, id).Scan(&name, &status, &public)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && public != 1) {
			// An unpublished tournament is indistinguishable from one that does
			// not exist, so the endpoint cannot be used to discover ids.
			httpx.Error(w, http.StatusNotFound, "not found", nil)
			return
		}
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not load results", err)
			return
		}
		// When the event is published on chess-results.com, that is the result —
		// the arbiter's upload is what players and federations treat as true,
		// and a second table typed in here would be wrong the moment a round
		// lands. Our own rounds are not served alongside it: chess-results
		// publishes a ranking, and pairing it with stale boards of ours would
		// invite exactly the disagreement this is meant to end.
		linked, err := linkedResultsFor(d, id)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not load results", err)
			return
		}
		if linked != nil {
			// The academy's workflow is Swiss-Manager → upload after every
			// round, so while the event is live this page follows the uploads
			// by itself: a stale read starts a refresh in the background and
			// serves the stored copy now — a parent's phone never waits on
			// chess-results.com, and the next poll gets the fresh table. The
			// floor in allowFetch keeps a hall full of phones at one upstream
			// fetch per interval.
			if cr != nil && !chessresults.FinalStage(linked.Stage) && staleExternal(linked.FetchedAt) {
				extID, crID := linked.extID, linked.ChessResID
				go func() {
					if err := cr.refreshExternal(extID, crID); err != nil &&
						!errors.Is(err, errExternalThrottled) {
						log.Printf("chessresults: public refresh %d: %v", crID, err)
					}
				}()
			}
			httpx.JSON(w, http.StatusOK, map[string]any{
				"tournament": map[string]any{"name": name, "status": status},
				"source":     linked.Source,
				"sourceUrl":  linked.URL,
				"stage":      linked.Stage,
				"fetchedAt":  linked.FetchedAt,
				"rounds":     publicExternalRounds(linked.Rounds),
				"standings":  publicExternalStandings(linked.Standings),
			})
			return
		}

		rounds, table, err := loadResults(d, id)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not load results", err)
			return
		}
		// Strip everything the public has no business seeing: the registration
		// ids are internal keys and the boards carry them too.
		pub := make([]map[string]any, 0, len(table))
		for _, row := range table {
			pub = append(pub, map[string]any{
				"rank": row.Rank, "name": row.Name, "category": row.Category,
				"points": row.Points, "played": row.Played,
				"wins": row.Wins, "draws": row.Draws, "losses": row.Losses,
				"buchholz": row.Buchholz,
			})
		}
		pubRounds := make([]map[string]any, 0, len(rounds))
		for _, rd := range rounds {
			boards := make([]map[string]any, 0, len(rd.Pairings))
			for _, p := range rd.Pairings {
				boards = append(boards, map[string]any{
					"board": p.Board, "white": p.WhiteN, "black": p.BlackN, "result": p.Result,
				})
			}
			pubRounds = append(pubRounds, map[string]any{
				"round": rd.Round, "status": rd.Status, "pairings": boards,
			})
		}
		httpx.JSON(w, http.StatusOK, map[string]any{
			"tournament": map[string]any{"name": name, "status": status},
			"rounds":     pubRounds, "standings": pub,
		})
	}
}

func handleCreateRound(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if requireStaff(d, w, r) == nil {
			return
		}
		tournamentID := r.PathValue("id")
		var exists int
		if err := d.QueryRow(`SELECT COUNT(*) FROM tournament WHERE tournament_id = ?`,
			tournamentID).Scan(&exists); err != nil || exists == 0 {
			httpx.Error(w, http.StatusNotFound, "not found", err)
			return
		}
		var next int
		if err := d.QueryRow(`SELECT COALESCE(MAX(round_no),0) + 1 FROM tournament_round
		                      WHERE tournament_id = ?`, tournamentID).Scan(&next); err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not add a round", err)
			return
		}
		roundID := newID("trd")
		if _, err := d.Exec(`INSERT INTO tournament_round (tournament_round_id, tournament_id, round_no, status)
		                     VALUES (?, ?, ?, 'Pending')`, roundID, tournamentID, next); err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not add a round", err)
			return
		}
		httpx.JSON(w, http.StatusCreated, map[string]any{"roundId": roundID, "round": next, "status": "Pending"})
	}
}

// handleProposePairings suggests a pairing for a round without saving it.
//
// A suggestion, not a pairing engine: a real Swiss uses the Dutch system with
// colour balance and float history, and this sorts by standing and pairs down
// the list avoiding rematches. It exists so an arbiter starts from a sensible
// list rather than a blank screen, and every board is editable before saving.
func handleProposePairings(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if requireStaff(d, w, r) == nil {
			return
		}
		tournamentID := r.PathValue("id")
		rounds, _, err := loadResults(d, tournamentID)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not load results", err)
			return
		}
		played := []standings.Pairing{}
		for _, rd := range rounds {
			for _, p := range rd.Pairings {
				played = append(played, standings.Pairing{Round: rd.Round, White: p.White, Black: p.Black, Result: p.Result})
			}
		}
		ids := []string{}
		names := map[string]string{}
		rows, err := d.Query(`SELECT tournament_registration_id, participant_name
		                      FROM tournament_registration WHERE tournament_id = ?`, tournamentID)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not load players", err)
			return
		}
		defer rows.Close()
		for rows.Next() {
			var id, name string
			if err := rows.Scan(&id, &name); err != nil {
				httpx.Error(w, http.StatusInternalServerError, "could not load players", err)
				return
			}
			ids = append(ids, id)
			names[id] = name
		}
		out := []map[string]any{}
		for i, p := range standings.Propose(ids, played) {
			out = append(out, map[string]any{
				"board": i + 1, "whiteRegistrationId": p.White, "white": names[p.White],
				"blackRegistrationId": p.Black, "black": names[p.Black], "result": p.Result,
			})
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"pairings": out})
	}
}

// handleSetPairings replaces a round's boards.
//
// A replace rather than a merge: an arbiter re-pairing a round has decided the
// old list is wrong, and leaving orphans behind would put a player on two
// boards at once.
func handleSetPairings(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if requireStaff(d, w, r) == nil {
			return
		}
		roundID := r.PathValue("roundId")
		var tournamentID string
		err := d.QueryRow(`SELECT tournament_id FROM tournament_round WHERE tournament_round_id = ?`,
			roundID).Scan(&tournamentID)
		if errors.Is(err, sql.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, "not found", nil)
			return
		}
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not load the round", err)
			return
		}
		var in struct {
			Pairings []struct {
				Board  int    `json:"board"`
				White  string `json:"whiteRegistrationId"`
				Black  string `json:"blackRegistrationId"`
				Result string `json:"result"`
			} `json:"pairings"`
		}
		if err := httpx.Decode(r, &in); err != nil {
			httpx.Error(w, http.StatusBadRequest, "pairings are required", err)
			return
		}

		// Every player must belong to this tournament, and nobody may appear
		// twice in the same round. Checked here rather than trusted, because a
		// board is what a result is later attached to.
		valid := map[string]bool{}
		rows, err := d.Query(`SELECT tournament_registration_id FROM tournament_registration
		                      WHERE tournament_id = ?`, tournamentID)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not load players", err)
			return
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err == nil {
				valid[id] = true
			}
		}
		rows.Close()

		seen := map[string]bool{}
		for _, p := range in.Pairings {
			if !valid[p.White] || (p.Black != "" && !valid[p.Black]) {
				httpx.Error(w, http.StatusBadRequest, "a player is not registered for this tournament", nil)
				return
			}
			if p.Black != "" && p.White == p.Black {
				httpx.Error(w, http.StatusBadRequest, "a player cannot be paired against themselves", nil)
				return
			}
			if seen[p.White] || (p.Black != "" && seen[p.Black]) {
				httpx.Error(w, http.StatusBadRequest, "a player appears on two boards in the same round", nil)
				return
			}
			seen[p.White] = true
			if p.Black != "" {
				seen[p.Black] = true
			}
			if p.Result != "" && !slices.Contains(resultCodes, p.Result) {
				httpx.Error(w, http.StatusBadRequest, "unknown result", nil)
				return
			}
		}

		tx, err := d.Begin()
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not save pairings", err)
			return
		}
		defer tx.Rollback()
		if _, err := tx.Exec(`DELETE FROM tournament_pairing WHERE tournament_round_id = ?`, roundID); err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not save pairings", err)
			return
		}
		for i, p := range in.Pairings {
			board := p.Board
			if board == 0 {
				board = i + 1
			}
			result := p.Result
			if result == "" {
				result = standings.Pending
			}
			var black any
			if p.Black != "" {
				black = p.Black
			}
			if _, err := tx.Exec(`
				INSERT INTO tournament_pairing
				  (tournament_pairing_id, tournament_round_id, board_no,
				   white_registration_id, black_registration_id, result)
				VALUES (?, ?, ?, ?, ?, ?)`,
				newID("tpr"), roundID, board, p.White, black, result); err != nil {
				httpx.Error(w, http.StatusInternalServerError, "could not save pairings", err)
				return
			}
		}
		if _, err := tx.Exec(`UPDATE tournament_round SET started_at = COALESCE(started_at, datetime('now'))
		                      WHERE tournament_round_id = ?`, roundID); err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not save pairings", err)
			return
		}
		if err := tx.Commit(); err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not save pairings", err)
			return
		}
		// A round of nothing but byes is finished the moment it is paired.
		refreshRoundStatus(d, roundID)
		httpx.JSON(w, http.StatusOK, map[string]any{"roundId": roundID, "boards": len(in.Pairings)})
	}
}

// handleRecordResult sets one board's result — the endpoint an arbiter hits
// most, between rounds, on a phone, in a noisy hall.
func handleRecordResult(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := requireStaff(d, w, r)
		if id == nil {
			return
		}
		var in struct {
			Result string `json:"result"`
		}
		if err := httpx.Decode(r, &in); err != nil {
			httpx.Error(w, http.StatusBadRequest, "result is required", err)
			return
		}
		result := strings.TrimSpace(in.Result)
		if !slices.Contains(resultCodes, result) {
			httpx.Error(w, http.StatusBadRequest,
				"result must be one of "+strings.Join(resultCodes, ", "), nil)
			return
		}
		pairingID := r.PathValue("pairingId")
		res, err := d.Exec(`UPDATE tournament_pairing
		                    SET result = ?, recorded_at = ?, recorded_by = ?
		                    WHERE tournament_pairing_id = ?`,
			result, time.Now().UTC().Format("2006-01-02 15:04:05"), id.UserAccountID, pairingID)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not record the result", err)
			return
		}
		if n, _ := res.RowsAffected(); n == 0 {
			httpx.Error(w, http.StatusNotFound, "not found", nil)
			return
		}
		var roundID string
		if err := d.QueryRow(`SELECT tournament_round_id FROM tournament_pairing
		                      WHERE tournament_pairing_id = ?`, pairingID).Scan(&roundID); err == nil {
			refreshRoundStatus(d, roundID)
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"result": result})
	}
}

// refreshRoundStatus derives a round's status from its boards: Playing while
// anything is outstanding, Completed once every result is in.
//
// Derived rather than set by hand so it cannot drift from what it describes —
// and it runs after pairings are saved as well as after a result is recorded,
// because a round of nothing but byes is finished the moment it is paired.
//
// A failure here is logged, not returned: the result the arbiter typed is
// already saved, and a stale status label is not worth failing their request
// over. An earlier version swallowed the error silently and hid a broken query.
func refreshRoundStatus(d *sql.DB, roundID string) {
	var pending, total int
	if err := d.QueryRow(`SELECT COUNT(*), COALESCE(SUM(result = 'Pending'), 0)
	                      FROM tournament_pairing WHERE tournament_round_id = ?`,
		roundID).Scan(&total, &pending); err != nil {
		log.Printf("tournament: round status for %s: %v", roundID, err)
		return
	}
	status, done := "Playing", "NULL"
	if total > 0 && pending == 0 {
		status, done = "Completed", "datetime('now')"
	}
	if _, err := d.Exec(`UPDATE tournament_round SET status = ?, completed_at = `+done+
		` WHERE tournament_round_id = ?`, status, roundID); err != nil {
		log.Printf("tournament: round status for %s: %v", roundID, err)
	}
}

func mountTournamentResults(mux *http.ServeMux, d *sql.DB, cr *chessResultsDeps) {
	const p = "/api/v1/tournaments"
	mux.HandleFunc("GET "+p+"/{id}/results", handleTournamentResults(d))
	mux.HandleFunc("POST "+p+"/{id}/rounds", handleCreateRound(d))
	mux.HandleFunc("GET "+p+"/{id}/proposed-pairings", handleProposePairings(d))
	mux.HandleFunc("PUT "+p+"/rounds/{roundId}/pairings", handleSetPairings(d))
	mux.HandleFunc("PATCH "+p+"/pairings/{pairingId}", handleRecordResult(d))

	// The one endpoint here that needs no session. Published events only, and
	// rate-limited because anything unauthenticated has to be.
	mux.HandleFunc("GET /api/v1/public/tournaments/{id}/results",
		httpx.RateLimit(60, handlePublicResults(d, cr)))
}
