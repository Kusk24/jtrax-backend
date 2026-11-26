// Gin routes for the staff draft wizard and its public, token-protected preview.
package tournaments

import (
	"database/sql"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/Kusk24/jtrax-backend/internal/accounts"
	"github.com/Kusk24/jtrax-backend/internal/auth"
	"github.com/Kusk24/jtrax-backend/internal/httpx"
)

func mountTournamentDraft(mux gin.IRoutes, d *sql.DB, cardPayments bool) {
	drafts := draftStore{d}
	mux.POST("/api/v1/tournaments/:id/preview", func(ctx *gin.Context) {
		if accounts.RequireStaff(d, ctx) == nil {
			return
		}
		token, err := drafts.previewToken(ctx.Param("id"))
		if err != nil {
			writeDraftError(ctx, err, http.StatusInternalServerError, "could not make a preview link")
			return
		}
		httpx.JSON(ctx, http.StatusOK, map[string]string{"token": token})
	})
	mux.POST("/api/v1/tournaments/:id/publish", func(ctx *gin.Context) {
		if accounts.RequireStaff(d, ctx) == nil {
			return
		}
		if err := drafts.publish(ctx.Param("id")); err != nil {
			writeDraftError(ctx, err, http.StatusInternalServerError, "could not publish the tournament")
			return
		}
		httpx.JSON(ctx, http.StatusOK, map[string]any{"published": true})
	})
	mux.DELETE("/api/v1/tournaments/:id/draft", func(ctx *gin.Context) {
		if accounts.RequireStaff(d, ctx) == nil {
			return
		}
		if err := drafts.discard(ctx.Param("id")); err != nil {
			writeDraftError(ctx, err, http.StatusConflict, "could not discard the draft")
			return
		}
		httpx.JSON(ctx, http.StatusOK, map[string]any{"discarded": true})
	})
	mux.GET("/api/v1/public/tournaments/:id/preview", httpx.RateLimit(60), func(ctx *gin.Context) {
		view, err := drafts.preview(ctx.Param("id"), ctx.Query("preview"), cardPayments)
		if errors.Is(err, errPreviewUnavailable) {
			httpx.Error(ctx, http.StatusNotFound, "not found", nil)
			return
		}
		if err != nil {
			httpx.Error(ctx, http.StatusInternalServerError, "could not load tournament", err)
			return
		}
		ctx.Header("Cache-Control", "no-store")
		httpx.JSON(ctx, http.StatusOK, view)
	})
}

func writeDraftError(ctx *gin.Context, err error, failureStatus int, failureMessage string) {
	switch {
	case errors.Is(err, errTournamentMissing):
		httpx.Error(ctx, http.StatusNotFound, "no such tournament", nil)
	case errors.Is(err, errTournamentLive):
		httpx.Error(ctx, http.StatusConflict, "this tournament is already live", nil)
	case errors.Is(err, errDiscardLive):
		httpx.Error(ctx, http.StatusConflict, "this tournament is live and cannot be discarded", nil)
	case errors.Is(err, errLoadDraft):
		httpx.Error(ctx, http.StatusInternalServerError, "could not load tournament", err)
	default:
		httpx.Error(ctx, failureStatus, failureMessage, err)
	}
}

// NotDraft hides unpublished tournaments from everyone but staff.
func NotDraft(*auth.Identity) (string, []any) { return "draft = 0", nil }
