// The tournament banner: an optional picture across the top of the public
// registration page and the parent's tournament card. With none uploaded the
// pages draw their own from the tournament's name, date and venue, so this is
// only ever the organiser's poster art replacing that.
package api

import (
	"database/sql"
	"errors"
	"io"
	"net/http"

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

func mountTournamentBanner(mux *http.ServeMux, d *sql.DB) {
	mux.HandleFunc("POST /api/v1/tournaments/{id}/banner", handleUploadBanner(d))
	mux.HandleFunc("DELETE /api/v1/tournaments/{id}/banner", handleDeleteBanner(d))
	mux.HandleFunc("GET /api/v1/tournaments/{id}/banner", httpx.RateLimit(120, handleGetBanner(d)))
}

func handleUploadBanner(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if requireStaff(d, w, r) == nil {
			return
		}
		tid := r.PathValue("id")
		if !tournamentExists(d, tid) {
			httpx.Error(w, http.StatusNotFound, "no such tournament", nil)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxBannerBytes)
		if err := r.ParseMultipartForm(maxBannerBytes); err != nil {
			httpx.Error(w, http.StatusRequestEntityTooLarge, "the picture is too large (5 MB max)", nil)
			return
		}
		file, _, err := r.FormFile("file")
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, "attach the banner as 'file'", nil)
			return
		}
		defer file.Close()
		data, err := io.ReadAll(file)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, "could not read the file", nil)
			return
		}
		// Sniffed from the bytes, never trusted from the request.
		mime := http.DetectContentType(data)
		if !bannerTypes[mime] {
			httpx.Error(w, http.StatusUnsupportedMediaType, "a banner can be a JPEG, PNG or WebP picture", nil)
			return
		}
		if _, err := d.Exec(
			`INSERT INTO tournament_banner (tournament_id, content_type, bytes, uploaded_at)
			 VALUES (?,?,?, datetime('now'))
			 ON CONFLICT(tournament_id) DO UPDATE SET
			   content_type = excluded.content_type, bytes = excluded.bytes,
			   uploaded_at = excluded.uploaded_at`,
			tid, mime, data); err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not store the banner", err)
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"saved": true, "bytes": len(data)})
	}
}

func handleDeleteBanner(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if requireStaff(d, w, r) == nil {
			return
		}
		if _, err := d.Exec(`DELETE FROM tournament_banner WHERE tournament_id = ?`, r.PathValue("id")); err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not remove the banner", err)
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"deleted": true})
	}
}

// handleGetBanner serves the picture. Anyone may see a live tournament's
// banner whose page is public; a signed-in user may see any live one (the
// parent's card shows events before registration opens); a draft's is for
// staff and its preview link only.
func handleGetBanner(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tid := r.PathValue("id")
		var draft, public int
		err := d.QueryRow(
			`SELECT draft, COALESCE(public_registration,0) + COALESCE(results_public,0)
			   FROM tournament WHERE tournament_id = ?`, tid).Scan(&draft, &public)
		if errors.Is(err, sql.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, "not found", nil)
			return
		}
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not load banner", err)
			return
		}
		allowed := draft == 0 && public > 0
		if !allowed {
			if draft == 1 {
				allowed = previewAllowed(d, r, tid)
			}
			if !allowed {
				id, idErr := auth.Lookup(d, bearerToken(r))
				allowed = idErr == nil && (isStaff(id.Role) || draft == 0)
			}
		}
		if !allowed {
			// Same answer as a missing tournament.
			httpx.Error(w, http.StatusNotFound, "not found", nil)
			return
		}
		var mime string
		var data []byte
		err = d.QueryRow(`SELECT content_type, bytes FROM tournament_banner WHERE tournament_id = ?`, tid).
			Scan(&mime, &data)
		if errors.Is(err, sql.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, "not found", nil)
			return
		}
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not load banner", err)
			return
		}
		w.Header().Set("Content-Type", mime)
		// Short: the organiser may swap the picture, and the pages add a
		// version to the address when they know it changed.
		w.Header().Set("Cache-Control", "private, max-age=60")
		_, _ = w.Write(data)
	}
}
