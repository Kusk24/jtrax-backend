// A tournament is reviewed before it goes live.
//
// The create wizard saves the event as a draft (draft = 1), then shows the
// organiser the real public registration page for it. That page is served by
// the portal, which has no staff session, so the draft is opened with a
// preview link instead: a random token held on the row and handed only to
// staff. The token dies at Publish, which is the one action that takes the
// event live — draft off, registration open — so a preview link that leaked
// can never become a way into a live form.
//
// A draft nobody publishes is thrown away whole by Discard: its categories,
// regulation and banner go with it.
package api

import (
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/http"

	"github.com/Kusk24/jtrax-backend/internal/auth"
	"github.com/Kusk24/jtrax-backend/internal/httpx"
)

func mountTournamentDraft(mux *http.ServeMux, d *sql.DB, cardPayments bool) {
	mux.HandleFunc("POST /api/v1/tournaments/{id}/preview", handleDraftPreviewLink(d))
	mux.HandleFunc("POST /api/v1/tournaments/{id}/publish", handlePublishDraft(d))
	mux.HandleFunc("DELETE /api/v1/tournaments/{id}/draft", handleDiscardDraft(d))
	// Public in the sense that it needs no session; the token is the
	// authorization, and it is 256 bits.
	mux.HandleFunc("GET /api/v1/public/tournaments/{id}/preview",
		httpx.RateLimit(60, handleDraftPreview(d, cardPayments)))
}

// previewAllowed reports whether the request carries the preview token of a
// tournament that is still a draft.
func previewAllowed(d *sql.DB, r *http.Request, tournamentID string) bool {
	given := r.URL.Query().Get("preview")
	if given == "" {
		return false
	}
	var token sql.NullString
	if err := d.QueryRow(
		`SELECT preview_token FROM tournament WHERE tournament_id = ? AND draft = 1`,
		tournamentID).Scan(&token); err != nil || !token.Valid || token.String == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(given), []byte(token.String)) == 1
}

// isDraft reports whether the tournament exists and is still a draft.
func isDraft(d *sql.DB, tournamentID string) (exists, draft bool, err error) {
	var flag int
	err = d.QueryRow(`SELECT draft FROM tournament WHERE tournament_id = ?`, tournamentID).Scan(&flag)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, nil
	}
	return err == nil, flag == 1, err
}

// handleDraftPreviewLink hands staff the draft's preview token, minting it the
// first time. The same token comes back on every call, so the page the
// organiser has open keeps working while they go back and edit.
func handleDraftPreviewLink(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if requireStaff(d, w, r) == nil {
			return
		}
		tid := r.PathValue("id")
		exists, draft, err := isDraft(d, tid)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not load tournament", err)
			return
		}
		if !exists {
			httpx.Error(w, http.StatusNotFound, "no such tournament", nil)
			return
		}
		if !draft {
			httpx.Error(w, http.StatusConflict, "this tournament is already live", nil)
			return
		}
		raw := make([]byte, 32)
		if _, err := rand.Read(raw); err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not make a preview link", err)
			return
		}
		if _, err := d.Exec(
			`UPDATE tournament SET preview_token = ? WHERE tournament_id = ? AND preview_token IS NULL`,
			hex.EncodeToString(raw), tid); err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not make a preview link", err)
			return
		}
		var token string
		if err := d.QueryRow(`SELECT preview_token FROM tournament WHERE tournament_id = ?`, tid).Scan(&token); err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not make a preview link", err)
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]string{"token": token})
	}
}

// handleDraftPreview is the public page's data for a draft, exactly as
// handlePublicTournament would serve it once live — plus "preview", so the
// page can stop anybody registering on it.
func handleDraftPreview(d *sql.DB, cardPayments bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tid := r.PathValue("id")
		// A wrong token, a live event and a missing one all look the same.
		if !previewAllowed(d, r, tid) {
			httpx.Error(w, http.StatusNotFound, "not found", nil)
			return
		}
		t, err := scanPublicTournament(d.QueryRow(publicTournamentSelect+`
			WHERE t.tournament_id = ? AND t.draft = 1`, tid))
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not load tournament", err)
			return
		}
		cats, err := publicCategories(d, tid)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not load tournament", err)
			return
		}
		t.CardPayments = cardPayments
		w.Header().Set("Cache-Control", "no-store")
		httpx.JSON(w, http.StatusOK, map[string]any{"tournament": t, "categories": cats, "preview": true})
	}
}

// handlePublishDraft takes a draft live: it stops being a draft, registration
// opens, and the preview link stops working.
func handlePublishDraft(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if requireStaff(d, w, r) == nil {
			return
		}
		tid := r.PathValue("id")
		res, err := d.Exec(
			`UPDATE tournament SET draft = 0, public_registration = 1, preview_token = NULL
			  WHERE tournament_id = ? AND draft = 1`, tid)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not publish the tournament", err)
			return
		}
		if n, _ := res.RowsAffected(); n == 0 {
			if exists, _, _ := isDraft(d, tid); !exists {
				httpx.Error(w, http.StatusNotFound, "no such tournament", nil)
				return
			}
			httpx.Error(w, http.StatusConflict, "this tournament is already live", nil)
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"published": true})
	}
}

// handleDiscardDraft throws an unpublished tournament away with everything
// the wizard attached to it. It refuses a live one: those are deleted from
// their own screen, where what goes with them is spelled out.
func handleDiscardDraft(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if requireStaff(d, w, r) == nil {
			return
		}
		tid := r.PathValue("id")
		exists, draft, err := isDraft(d, tid)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not load tournament", err)
			return
		}
		if !exists {
			httpx.Error(w, http.StatusNotFound, "no such tournament", nil)
			return
		}
		if !draft {
			httpx.Error(w, http.StatusConflict, "this tournament is live and cannot be discarded", nil)
			return
		}
		err = inTx(d, func(tx *sql.Tx) error {
			for _, q := range []string{
				`DELETE FROM tournament_category WHERE tournament_id = ?`,
				`DELETE FROM tournament_regulation WHERE tournament_id = ?`,
				`DELETE FROM tournament_banner WHERE tournament_id = ?`,
				`DELETE FROM tournament WHERE tournament_id = ? AND draft = 1`,
			} {
				if _, err := tx.Exec(q, tid); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			httpx.Error(w, http.StatusConflict, "could not discard the draft", err)
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"discarded": true})
	}
}

// notDraft hides unpublished tournaments from everyone but staff.
func notDraft(*auth.Identity) (string, []any) { return "draft = 0", nil }
