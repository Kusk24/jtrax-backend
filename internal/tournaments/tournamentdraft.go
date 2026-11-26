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
package tournaments

import (
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/Kusk24/jtrax-backend/internal/accounts"
	"github.com/Kusk24/jtrax-backend/internal/auth"
	"github.com/Kusk24/jtrax-backend/internal/db"
	"github.com/Kusk24/jtrax-backend/internal/httpx"
)

func mountTournamentDraft(mux gin.IRoutes, d *sql.DB, cardPayments bool) {
	mux.POST("/api/v1/tournaments/:id/preview", handleDraftPreviewLink(d))
	mux.POST("/api/v1/tournaments/:id/publish", handlePublishDraft(d))
	mux.DELETE("/api/v1/tournaments/:id/draft", handleDiscardDraft(d))
	// Public in the sense that it needs no session; the token is the
	// authorization, and it is 256 bits.
	mux.GET("/api/v1/public/tournaments/:id/preview",
		httpx.RateLimit(60), handleDraftPreview(d, cardPayments))
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
func handleDraftPreviewLink(d *sql.DB) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		if accounts.RequireStaff(d, ctx) == nil {
			return
		}
		tid := ctx.Param("id")
		exists, draft, err := isDraft(d, tid)
		if err != nil {
			httpx.Error(ctx, http.StatusInternalServerError, "could not load tournament", err)
			return
		}
		if !exists {
			httpx.Error(ctx, http.StatusNotFound, "no such tournament", nil)
			return
		}
		if !draft {
			httpx.Error(ctx, http.StatusConflict, "this tournament is already live", nil)
			return
		}
		raw := make([]byte, 32)
		if _, err := rand.Read(raw); err != nil {
			httpx.Error(ctx, http.StatusInternalServerError, "could not make a preview link", err)
			return
		}
		if _, err := d.Exec(
			`UPDATE tournament SET preview_token = ? WHERE tournament_id = ? AND preview_token IS NULL`,
			hex.EncodeToString(raw), tid); err != nil {
			httpx.Error(ctx, http.StatusInternalServerError, "could not make a preview link", err)
			return
		}
		var token string
		if err := d.QueryRow(`SELECT preview_token FROM tournament WHERE tournament_id = ?`, tid).Scan(&token); err != nil {
			httpx.Error(ctx, http.StatusInternalServerError, "could not make a preview link", err)
			return
		}
		httpx.JSON(ctx, http.StatusOK, map[string]string{"token": token})
	}
}

// handleDraftPreview is the public page's data for a draft, exactly as
// handlePublicTournament would serve it once live — plus "preview", so the
// page can stop anybody registering on it.
func handleDraftPreview(d *sql.DB, cardPayments bool) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		tid := ctx.Param("id")
		// A wrong token, a live event and a missing one all look the same.
		if !previewAllowed(d, ctx.Request, tid) {
			httpx.Error(ctx, http.StatusNotFound, "not found", nil)
			return
		}
		t, err := scanPublicTournament(d.QueryRow(publicTournamentSelect+`
			WHERE t.tournament_id = ? AND t.draft = 1`, tid))
		if err != nil {
			httpx.Error(ctx, http.StatusInternalServerError, "could not load tournament", err)
			return
		}
		cats, err := publicCategories(d, tid)
		if err != nil {
			httpx.Error(ctx, http.StatusInternalServerError, "could not load tournament", err)
			return
		}
		t.CardPayments = cardPayments
		ctx.Header("Cache-Control", "no-store")
		httpx.JSON(ctx, http.StatusOK, map[string]any{"tournament": t, "categories": cats, "preview": true})
	}
}

// handlePublishDraft takes a draft live: it stops being a draft, registration
// opens, and the preview link stops working.
func handlePublishDraft(d *sql.DB) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		if accounts.RequireStaff(d, ctx) == nil {
			return
		}
		tid := ctx.Param("id")
		res, err := d.Exec(
			`UPDATE tournament SET draft = 0, public_registration = 1, preview_token = NULL
			  WHERE tournament_id = ? AND draft = 1`, tid)
		if err != nil {
			httpx.Error(ctx, http.StatusInternalServerError, "could not publish the tournament", err)
			return
		}
		if n, _ := res.RowsAffected(); n == 0 {
			if exists, _, _ := isDraft(d, tid); !exists {
				httpx.Error(ctx, http.StatusNotFound, "no such tournament", nil)
				return
			}
			httpx.Error(ctx, http.StatusConflict, "this tournament is already live", nil)
			return
		}
		httpx.JSON(ctx, http.StatusOK, map[string]any{"published": true})
	}
}

// handleDiscardDraft throws an unpublished tournament away with everything
// the wizard attached to it. It refuses a live one: those are deleted from
// their own screen, where what goes with them is spelled out.
func handleDiscardDraft(d *sql.DB) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		if accounts.RequireStaff(d, ctx) == nil {
			return
		}
		tid := ctx.Param("id")
		exists, draft, err := isDraft(d, tid)
		if err != nil {
			httpx.Error(ctx, http.StatusInternalServerError, "could not load tournament", err)
			return
		}
		if !exists {
			httpx.Error(ctx, http.StatusNotFound, "no such tournament", nil)
			return
		}
		if !draft {
			httpx.Error(ctx, http.StatusConflict, "this tournament is live and cannot be discarded", nil)
			return
		}
		err = db.InTransaction(d, func(tx *sql.Tx) error {
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
			httpx.Error(ctx, http.StatusConflict, "could not discard the draft", err)
			return
		}
		httpx.JSON(ctx, http.StatusOK, map[string]any{"discarded": true})
	}
}

// NotDraft hides unpublished tournaments from everyone but staff.
func NotDraft(*auth.Identity) (string, []any) { return "draft = 0", nil }
