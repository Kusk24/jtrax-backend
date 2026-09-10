// The tournament regulation file: organisers hand the academy a PDF, staff
// attach it to the tournament, and the public registration page serves it so
// a parent can read what they are signing a child up for.
package api

import (
	"database/sql"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"

	"github.com/Kusk24/jtrax-backend/internal/auth"
	"github.com/Kusk24/jtrax-backend/internal/httpx"
)

// maxRegulationBytes caps an upload at 5 MB — a scanned regulation runs one
// or two, and the whole file sits in a database row.
const maxRegulationBytes = 5 << 20

func mountTournamentRegulation(mux *http.ServeMux, d *sql.DB) {
	mux.HandleFunc("POST /api/v1/tournaments/{id}/regulation", handleUploadRegulation(d))
	mux.HandleFunc("DELETE /api/v1/tournaments/{id}/regulation", handleDeleteRegulation(d))
	// Public — the registration page links it — so it is rate-limited like
	// every other unauthenticated route.
	mux.HandleFunc("GET /api/v1/tournaments/{id}/regulation", httpx.RateLimit(60, handleGetRegulation(d)))
}

// regulationTypes is what a regulation can actually be: the PDF organisers
// send, or a photographed page. Anything else is refused — this endpoint must
// never become a way to host arbitrary files on the academy's domain.
var regulationTypes = map[string]bool{
	"application/pdf": true,
	"image/jpeg":      true,
	"image/png":       true,
	"image/webp":      true,
}

func handleUploadRegulation(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := requireIdentity(d, w, r)
		if id == nil {
			return
		}
		if !isStaff(id.Role) {
			httpx.Error(w, http.StatusForbidden, "only admin or reception may attach a regulation", nil)
			return
		}
		tournamentID := r.PathValue("id")
		if !tournamentExists(d, tournamentID) {
			httpx.Error(w, http.StatusNotFound, "no such tournament", nil)
			return
		}

		r.Body = http.MaxBytesReader(w, r.Body, maxRegulationBytes)
		if err := r.ParseMultipartForm(maxRegulationBytes); err != nil {
			httpx.Error(w, http.StatusRequestEntityTooLarge, "the file is too large (5 MB max)", nil)
			return
		}
		file, header, err := r.FormFile("file")
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, "attach the regulation as 'file'", nil)
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
		if !regulationTypes[mime] {
			httpx.Error(w, http.StatusUnsupportedMediaType, "regulations can be a PDF or a photo (JPEG, PNG, WebP)", nil)
			return
		}

		if _, err := d.Exec(
			`INSERT INTO tournament_regulation (tournament_id, filename, content_type, bytes, uploaded_at)
			 VALUES (?,?,?,?, datetime('now'))
			 ON CONFLICT(tournament_id) DO UPDATE SET
			   filename = excluded.filename, content_type = excluded.content_type,
			   bytes = excluded.bytes, uploaded_at = excluded.uploaded_at`,
			tournamentID, safeFilename(header.Filename), mime, data); err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not store the regulation", err)
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"saved": true, "bytes": len(data)})
	}
}

func handleDeleteRegulation(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := requireIdentity(d, w, r)
		if id == nil {
			return
		}
		if !isStaff(id.Role) {
			httpx.Error(w, http.StatusForbidden, "only admin or reception may remove a regulation", nil)
			return
		}
		if _, err := d.Exec(`DELETE FROM tournament_regulation WHERE tournament_id = ?`, r.PathValue("id")); err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not remove the regulation", err)
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"deleted": true})
	}
}

// handleGetRegulation serves the file. Public only where the tournament
// itself is public — an event with registration and results both switched off
// keeps its paperwork to signed-in staff.
func handleGetRegulation(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tournamentID := r.PathValue("id")
		var isPublic int
		err := d.QueryRow(
			`SELECT COALESCE(public_registration,0) + COALESCE(results_public,0)
			   FROM tournament WHERE tournament_id = ?`, tournamentID).Scan(&isPublic)
		if errors.Is(err, sql.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, "not found", nil)
			return
		}
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not load regulation", err)
			return
		}
		if isPublic == 0 {
			id, idErr := auth.Lookup(d, bearerToken(r))
			if idErr != nil || !isStaff(id.Role) {
				// Same answer as a missing tournament, so this cannot probe
				// which private events exist.
				httpx.Error(w, http.StatusNotFound, "not found", nil)
				return
			}
		}

		var filename, mime string
		var data []byte
		err = d.QueryRow(
			`SELECT filename, content_type, bytes FROM tournament_regulation WHERE tournament_id = ?`,
			tournamentID).Scan(&filename, &mime, &data)
		if errors.Is(err, sql.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, "not found", nil)
			return
		}
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not load regulation", err)
			return
		}
		w.Header().Set("Content-Type", mime)
		// Inline: a parent taps the link and reads; the browser only offers a
		// download for types it cannot show.
		w.Header().Set("Content-Disposition", `inline; filename="`+filename+`"`)
		w.Header().Set("Cache-Control", "public, max-age=300")
		_, _ = w.Write(data)
	}
}

func tournamentExists(d *sql.DB, id string) bool {
	var n int
	d.QueryRow(`SELECT COUNT(*) FROM tournament WHERE tournament_id = ?`, id).Scan(&n)
	return n > 0
}

// safeFilename keeps a name the browser can echo into Content-Disposition
// without header injection: printable, no quotes, no path, bounded.
var unsafeFilenameChars = regexp.MustCompile(`[^A-Za-z0-9 ._()\p{L}-]`)

func safeFilename(name string) string {
	name = strings.TrimSpace(name)
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	name = unsafeFilenameChars.ReplaceAllString(name, "_")
	if len(name) > 120 {
		name = name[len(name)-120:]
	}
	if name == "" {
		name = "regulation"
	}
	return name
}
