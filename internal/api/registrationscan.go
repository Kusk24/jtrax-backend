// Scanning a paper registration form: the front desk photographs the form, and
// the console gets back the fields to pre-fill the registration wizard with.
//
// Nothing here writes to the database. The reply is a suggestion a member of
// staff corrects and confirms — a misread date of birth or phone number is
// worse than a blank one, and the form is a child's identity record.
//
// The image is held in memory for the length of the call and then dropped. It
// is not written to disk, not logged, and not kept: the academy's copy of
// record is the piece of paper.
package api

import (
	"database/sql"
	"io"
	"net/http"

	"github.com/Kusk24/jtrax-backend/internal/httpx"
	"github.com/Kusk24/jtrax-backend/internal/ocr"
)

// A phone photo of an A4 page is a couple of megabytes; ten is generous and
// still bounds what one request can make the server hold.
const maxScanBytes = 10 << 20

// What a camera or a scanner produces. Anything else is refused before it
// reaches the provider, so a mis-picked file fails here and costs nothing.
var scannableTypes = map[string]bool{
	"image/jpeg": true,
	"image/png":  true,
	"image/webp": true,
	"image/heic": true,
	"image/heif": true,
}

func mountRegistrationScan(mux *http.ServeMux, d *sql.DB, provider ocr.Provider) {
	// Rate-limited even though it is authenticated: every call costs money at
	// the provider, so a stuck client retrying is a bill as well as load.
	mux.HandleFunc("POST /api/v1/registrations/scan",
		httpx.RateLimit(30, handleScanRegistration(d, provider)))
}

func handleScanRegistration(d *sql.DB, provider ocr.Provider) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := requireIdentity(d, w, r)
		if id == nil {
			return
		}
		// Registration is front-desk work. A parent or student has no reason to
		// be able to post a photograph to a paid vision API.
		if !isStaff(id.Role) {
			httpx.Error(w, http.StatusForbidden, "only admin or reception may scan a form", nil)
			return
		}
		if provider == nil {
			httpx.Error(w, http.StatusServiceUnavailable,
				"form scanning is not configured on this server", nil)
			return
		}

		// Cap what can be read before parsing, not after: without this the
		// server buffers whatever was sent.
		r.Body = http.MaxBytesReader(w, r.Body, maxScanBytes)
		if err := r.ParseMultipartForm(maxScanBytes); err != nil {
			httpx.Error(w, http.StatusRequestEntityTooLarge,
				"image is too large (10 MB maximum)", err)
			return
		}
		defer r.MultipartForm.RemoveAll()

		file, header, err := r.FormFile("image")
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
				"upload a photo of the form (JPEG, PNG, WebP or HEIC)", nil)
			return
		}

		form, err := provider.Extract(r.Context(), data, mime)
		if err != nil {
			// The provider's error can quote the request, which is the child's
			// data, so it stays internal and the caller gets a plain sentence.
			httpx.Error(w, http.StatusBadGateway,
				"could not read the form — try a clearer, straighter photo", err)
			return
		}

		httpx.JSON(w, http.StatusOK, map[string]any{
			"fields": form,
			// Named so it is answerable which service saw the data, and echoed
			// back so the console can show it beside the results.
			"provider": provider.Name(),
			"filename": header.Filename,
			// Said out loud in the payload because it is the whole contract of
			// this endpoint: staff confirm, the server did not save anything.
			"saved": false,
		})
	}
}
