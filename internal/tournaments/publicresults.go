// The public results page reads only published events and exposes player names and scores.
package tournaments

import (
	"database/sql"
	"errors"
	"log"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/Kusk24/jtrax-backend/internal/chessresults"
	"github.com/Kusk24/jtrax-backend/internal/httpx"
)

func handlePublicResults(d *sql.DB, cr *chessResultsDeps) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		id := ctx.Param("id")
		header, err := publicResultHeader(d, id)
		if err != nil {
			httpx.Error(ctx, http.StatusInternalServerError, "could not load results", err)
			return
		}
		if header == nil {
			httpx.Error(ctx, http.StatusNotFound, "not found", nil)
			return
		}
		result, err := publicResultTables(d, cr, id)
		if err != nil {
			httpx.Error(ctx, http.StatusInternalServerError, "could not load results", err)
			return
		}
		result["tournament"] = header
		httpx.JSON(ctx, http.StatusOK, result)
	}
}

// A missing or unpublished event has no public header.
func publicResultHeader(d *sql.DB, id string) (map[string]any, error) {
	var name, status, start, end, venueName, venueAddress string
	var public, hasBanner int
	err := d.QueryRow(`SELECT name, tournament_status, results_public,
		                          COALESCE(start_date,''), COALESCE(end_date,''),
		                          COALESCE(venue_name,''), COALESCE(venue_address,''),
		                          EXISTS (SELECT 1 FROM tournament_banner b WHERE b.tournament_id = tournament.tournament_id)
		                   FROM tournament WHERE tournament_id = ?`, id).
		Scan(&name, &status, &public, &start, &end, &venueName, &venueAddress, &hasBanner)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && public != 1) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"name": name, "status": status, "startDate": start, "endDate": end,
		"venueName": venueName, "venueAddress": venueAddress, "hasBanner": hasBanner == 1,
	}, nil
}

// The arbiter's linked results take precedence over the locally recorded boards.
func publicResultTables(d *sql.DB, cr *chessResultsDeps, id string) (map[string]any, error) {
	sections, err := publicSections(d, cr, id)
	if err != nil {
		return nil, err
	}
	if len(sections) > 0 {
		first := sections[0]
		// Older clients read the first section from the top level.
		return map[string]any{"source": "chess-results", "sections": sections,
			"sourceUrl": first["sourceUrl"], "stage": first["stage"], "fetchedAt": first["fetchedAt"],
			"rounds": first["rounds"], "standings": first["standings"]}, nil
	}
	linked, err := linkedResultsFor(d, id)
	if err != nil {
		return nil, err
	}
	if linked != nil {
		result := linked.publicFields(cr)
		result["source"] = linked.Source
		return result, nil
	}
	rounds, table, err := loadResults(d, id)
	if err != nil {
		return nil, err
	}
	return publicLocalTables(rounds, table), nil
}

func publicLocalTables(rounds []roundView, table []standingView) map[string]any {
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
	return map[string]any{"rounds": pubRounds, "standings": pub}
}

func (linked *linkedResults) publicFields(cr *chessResultsDeps) map[string]any {
	// Serve the stored table immediately; refresh a stale live event in the background.
	if cr != nil && !chessresults.FinalStage(linked.Stage) && staleExternal(linked.FetchedAt) {
		extID, crID := linked.extID, linked.ChessResID
		go func() {
			if err := cr.refreshExternal(extID, crID); err != nil && !errors.Is(err, errExternalThrottled) {
				log.Printf("chessresults: public refresh %d: %v", crID, err)
			}
		}()
	}
	return map[string]any{"sourceUrl": linked.URL, "stage": linked.Stage, "fetchedAt": linked.FetchedAt, "rounds": publicExternalRounds(linked.Rounds), "standings": publicExternalStandings(linked.Standings)}
}

// publicSections is a connected tournament's chess-results categories as the
// public page reads them: name, where it came from, the rounds and the table.
// Empty when the tournament is not connected. A category never read yet is
// left out rather than shown as an empty table.
//
// A stale category starts a background re-read, as the single-link path does,
// so a hall full of phones keeps the page current without waiting on it.
func publicSections(d *sql.DB, cr *chessResultsDeps, tournamentID string) ([]map[string]any, error) {
	view, err := loadResultSections(d, tournamentID)
	if err != nil || !view.Connected {
		return nil, err
	}
	out := []map[string]any{}
	for _, sec := range view.Sections {
		linked, err := linkedResultsForID(d, sql.NullInt64{Int64: int64(sec.ChessResultsID), Valid: true})
		if err != nil {
			return nil, err
		}
		if linked == nil {
			continue
		}
		fields := linked.publicFields(cr)
		fields["name"] = sec.Name
		out = append(out, fields)
	}
	return out, nil
}
