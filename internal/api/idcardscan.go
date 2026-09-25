// Reading a player's ID card on the public entry form, so a parent types a name
// and a date of birth once instead of twice.
//
// # This is the first unauthenticated endpoint that costs money
//
// Everything else a stranger can reach is a database read or a row insert. This
// one calls a paid vision API, which makes an idle abuser's cost our cost. Four
// things bound it, and they are the reason it is a separate file from the
// staff-side scanner rather than a role check inside it:
//
//   - It only answers for a tournament that is **open to public registration**.
//     A closed event 404s, exactly as the entry endpoint does, so the surface
//     exists only while the academy is actually taking entries.
//   - It is rate-limited harder than any other route (5/min against the 10 the
//     entry form gets), because a scan is seconds of provider time where an
//     insert is microseconds of ours.
//   - The image is capped before it is read, so one request cannot make the
//     server hold an arbitrary amount.
//   - The reply is a **suggestion**. Nothing is written, nothing is trusted,
//     and an entrant who skips the scan enters exactly as before — so turning
//     this off, or having it fail, costs nobody their entry.
//
// # Nothing is kept
//
// The bytes are read, sent, and dropped with the request. There is no column
// for them and no file written: a store of children's identity documents is a
// thing to leak, and a thing somebody would later have to be asked to delete.
// What survives the call is a name and a date of birth, in a form field the
// entrant can see and correct before they submit.
package api

import (
	"database/sql"
	"io"
	"net/http"

	"github.com/Kusk24/jtrax-backend/internal/httpx"
	"github.com/Kusk24/jtrax-backend/internal/ocr"
)

func mountIDCardScan(mux *http.ServeMux, d *sql.DB, provider ocr.Provider) {
	mux.HandleFunc("POST /api/v1/public/tournaments/{id}/scan-id",
		httpx.RateLimit(5, handleScanIDCard(d, provider)))
}

func handleScanIDCard(d *sql.DB, provider ocr.Provider) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// The gate that makes this endpoint exist at all. Same 404-not-403 as
		// the entry form: a closed event must not be distinguishable from one
		// that does not exist, or this becomes a way to enumerate tournaments.
		var open int
		err := d.QueryRow(`SELECT COUNT(*) FROM tournament
		                   WHERE tournament_id = ? AND public_registration = 1`,
			r.PathValue("id")).Scan(&open)
		if err != nil || open == 0 {
			httpx.Error(w, http.StatusNotFound, "not found", nil)
			return
		}

		provider := scannerFor(d, provider)
		reader, ok := provider.(ocr.IDCardReader)
		if provider == nil || !ok {
			// Not an error the entrant caused, and not one they can fix. The
			// form falls back to typing, which is what it did before.
			httpx.Error(w, http.StatusServiceUnavailable,
				"card reading is not available — please type your details", nil)
			return
		}

		if !readScanUpload(w, r) {
			return
		}
		defer r.MultipartForm.RemoveAll()

		file, _, err := r.FormFile("image")
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, "expected an image field named 'image'", err)
			return
		}
		defer file.Close()

		data, err := io.ReadAll(file)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, "could not read the uploaded image", err)
			return
		}
		if len(data) == 0 {
			httpx.Error(w, http.StatusBadRequest, "the uploaded image is empty", nil)
			return
		}

		// Sniff the bytes rather than trust the declared type: the browser's
		// content type is caller-supplied, and this decides what is sent on to
		// a third party.
		mime := http.DetectContentType(data)
		if !scannableTypes[mime] {
			httpx.Error(w, http.StatusUnsupportedMediaType,
				"upload a photo of the card (JPEG, PNG, WebP or HEIC)", nil)
			return
		}

		card, err := reader.ExtractIDCard(r.Context(), data, mime)
		if err != nil {
			// The provider's error can quote the request, which is the child's
			// identity document, so it stays internal.
			httpx.Error(w, http.StatusBadGateway,
				"could not read the card — try a clearer, straighter photo, or type your details", err)
			return
		}

		httpx.JSON(w, http.StatusOK, map[string]any{
			"fields": card,
			// Answerable which service saw the document.
			"provider": provider.Name(),
		})
	}
}
