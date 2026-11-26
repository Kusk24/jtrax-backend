// Tournament registration, results and payment routes.
package tournaments

import (
	"database/sql"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/Kusk24/jtrax-backend/internal/httpx"
	"github.com/Kusk24/jtrax-backend/internal/ocr"
)

func MountIDCardScan(mux gin.IRoutes, d *sql.DB, provider ocr.Provider) {
	mux.POST("/api/v1/public/tournaments/:id/scan-id",
		httpx.RateLimit(5), handleScanIDCard(d, provider))
	// The parent portal's own scan, for one of the caller's children.
	mux.POST("/api/v1/tournaments/:id/scan-id",
		httpx.RateLimit(5), handleParentScanIDCard(d, provider))
}

func handleScanIDCard(d *sql.DB, provider ocr.Provider) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		// The gate that makes this endpoint exist at all. Same 404-not-403 as
		// the entry form: a closed event must not be distinguishable from one
		// that does not exist, or this becomes a way to enumerate tournaments.
		var open int
		err := d.QueryRow(`SELECT COUNT(*) FROM tournament
		                   WHERE tournament_id = ? AND public_registration = 1 AND draft = 0`,
			ctx.Param("id")).Scan(&open)
		if err != nil || open == 0 {
			httpx.Error(ctx, http.StatusNotFound, "not found", nil)
			return
		}

		card := readIDCard(d, provider, ctx)
		if card == nil {
			return
		}
		// What the card said is kept for the entry; the photo is not.
		checkID, err := saveIDCheck(d, ctx.Param("id"), "", card)
		if err != nil {
			httpx.Error(ctx, http.StatusInternalServerError, "could not save the ID card check", err)
			return
		}

		httpx.JSON(ctx, http.StatusOK, map[string]any{
			"fields": card,
			// Named by the entry: the date of birth the age group is
			// decided by comes from here, not from the form.
			"checkId": checkID,
			// Answerable which service saw the document.
			"provider": provider.Name(),
		})
	}
}
