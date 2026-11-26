// The tournament banner: an optional picture across the top of the public
// registration page and the parent's tournament card. With none uploaded the
// pages draw their own from the tournament's name, date and venue, so this is
// only ever the organiser's poster art replacing that.
package tournaments

import (
	"database/sql"
	"errors"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/Kusk24/jtrax-backend/internal/accounts"
	"github.com/Kusk24/jtrax-backend/internal/auth"
	"github.com/Kusk24/jtrax-backend/internal/httpx"
)

// maxBannerBytes caps an upload at 5 MB, like the regulation — a wide poster
// exported at a sensible size is well under one.
const maxBannerBytes = 5 << 20

// bannerTypes is pictures only. A banner is drawn with <img>, so a PDF would
// never show, and nothing else may be hosted here.
var bannerTypes = map[string]bool{
	"image/jpeg": true,
	"image/png":  true,
	"image/webp": true,
}

func mountTournamentBanner(mux gin.IRoutes, d *sql.DB) {
	mux.POST("/api/v1/tournaments/:id/banner", handleUploadBanner(d))
	mux.DELETE("/api/v1/tournaments/:id/banner", handleDeleteBanner(d))
	mux.GET("/api/v1/tournaments/:id/banner", httpx.RateLimit(120), handleGetBanner(d))
}

func handleUploadBanner(d *sql.DB) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		if accounts.RequireStaff(d, ctx) == nil {
			return
		}
		tid := ctx.Param("id")
		if !tournamentExists(d, tid) {
			httpx.Error(ctx, http.StatusNotFound, "no such tournament", nil)
			return
		}
		ctx.Request.Body = http.MaxBytesReader(ctx.Writer, ctx.Request.Body, maxBannerBytes)
		if err := ctx.Request.ParseMultipartForm(maxBannerBytes); err != nil {
			httpx.Error(ctx, http.StatusRequestEntityTooLarge, "the picture is too large (5 MB max)", nil)
			return
		}
		file, _, err := ctx.Request.FormFile("file")
		if err != nil {
			httpx.Error(ctx, http.StatusBadRequest, "attach the banner as 'file'", nil)
			return
		}
		defer file.Close()
		data, err := io.ReadAll(file)
		if err != nil {
			httpx.Error(ctx, http.StatusBadRequest, "could not read the file", nil)
			return
		}
		// Sniffed from the bytes, never trusted from the request.
		mime := http.DetectContentType(data)
		if !bannerTypes[mime] {
			httpx.Error(ctx, http.StatusUnsupportedMediaType, "a banner can be a JPEG, PNG or WebP picture", nil)
			return
		}
		if _, err := d.Exec(
			`INSERT INTO tournament_banner (tournament_id, content_type, bytes, uploaded_at)
			 VALUES (?,?,?, datetime('now'))
			 ON CONFLICT(tournament_id) DO UPDATE SET
			   content_type = excluded.content_type, bytes = excluded.bytes,
			   uploaded_at = excluded.uploaded_at`,
			tid, mime, data); err != nil {
			httpx.Error(ctx, http.StatusInternalServerError, "could not store the banner", err)
			return
		}
		httpx.JSON(ctx, http.StatusOK, map[string]any{"saved": true, "bytes": len(data)})
	}
}

func handleDeleteBanner(d *sql.DB) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		if accounts.RequireStaff(d, ctx) == nil {
			return
		}
		if _, err := d.Exec(`DELETE FROM tournament_banner WHERE tournament_id = ?`, ctx.Param("id")); err != nil {
			httpx.Error(ctx, http.StatusInternalServerError, "could not remove the banner", err)
			return
		}
		httpx.JSON(ctx, http.StatusOK, map[string]any{"deleted": true})
	}
}

// handleGetBanner serves the picture. Anyone may see a live tournament's
// banner whose page is public; a signed-in user may see any live one (the
// parent's card shows events before registration opens); a draft's is for
// staff and its preview link only.
func handleGetBanner(d *sql.DB) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		tid := ctx.Param("id")
		var draft, public int
		err := d.QueryRow(
			`SELECT draft, COALESCE(public_registration,0) + COALESCE(results_public,0)
			   FROM tournament WHERE tournament_id = ?`, tid).Scan(&draft, &public)
		if errors.Is(err, sql.ErrNoRows) {
			httpx.Error(ctx, http.StatusNotFound, "not found", nil)
			return
		}
		if err != nil {
			httpx.Error(ctx, http.StatusInternalServerError, "could not load banner", err)
			return
		}
		allowed := draft == 0 && public > 0
		if !allowed {
			if draft == 1 {
				allowed = previewAllowed(d, ctx.Request, tid)
			}
			if !allowed {
				id, idErr := auth.Lookup(d, accounts.BearerToken(ctx.Request))
				allowed = idErr == nil && (accounts.IsStaff(id.Role) || draft == 0)
			}
		}
		if !allowed {
			// Same answer as a missing tournament.
			httpx.Error(ctx, http.StatusNotFound, "not found", nil)
			return
		}
		var mime string
		var data []byte
		err = d.QueryRow(`SELECT content_type, bytes FROM tournament_banner WHERE tournament_id = ?`, tid).
			Scan(&mime, &data)
		if errors.Is(err, sql.ErrNoRows) {
			httpx.Error(ctx, http.StatusNotFound, "not found", nil)
			return
		}
		if err != nil {
			httpx.Error(ctx, http.StatusInternalServerError, "could not load banner", err)
			return
		}
		ctx.Header("Content-Type", mime)
		// Checked again on every view: the organiser may swap the picture
		// (its prize, its dates), and the public page must show the new one
		// at once, not after a minute in someone's browser cache.
		ctx.Header("Cache-Control", "no-cache")
		_, _ = ctx.Writer.Write(data)
	}
}
