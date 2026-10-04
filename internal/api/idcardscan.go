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
//   - The scan is required: the entry's date of birth, and so its age group,
//     comes from what the card said (idcheck.go).
//
// # Nothing is kept
//
// The bytes are read, sent, and dropped with the request. There is no column
// for them and no file written: a store of children's identity documents is a
// thing to leak, and a thing somebody would later have to be asked to delete.
// What survives the call is what was read — the date of birth, the name, the
// document type — in id_card_check, until the entry uses it.
package api

import (
	"database/sql"
	"net/http"

	"github.com/Kusk24/jtrax-backend/internal/httpx"
	"github.com/Kusk24/jtrax-backend/internal/ocr"
)

func mountIDCardScan(mux *http.ServeMux, d *sql.DB, provider ocr.Provider) {
	mux.HandleFunc("POST /api/v1/public/tournaments/{id}/scan-id",
		httpx.RateLimit(5, handleScanIDCard(d, provider)))
	// The parent portal's own scan, for one of the caller's children.
	mux.HandleFunc("POST /api/v1/tournaments/{id}/scan-id",
		httpx.RateLimit(5, handleParentScanIDCard(d, provider)))
}

func handleScanIDCard(d *sql.DB, provider ocr.Provider) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// The gate that makes this endpoint exist at all. Same 404-not-403 as
		// the entry form: a closed event must not be distinguishable from one
		// that does not exist, or this becomes a way to enumerate tournaments.
		var open int
		err := d.QueryRow(`SELECT COUNT(*) FROM tournament
		                   WHERE tournament_id = ? AND public_registration = 1 AND draft = 0`,
			r.PathValue("id")).Scan(&open)
		if err != nil || open == 0 {
			httpx.Error(w, http.StatusNotFound, "not found", nil)
			return
		}

		card := readIDCard(d, provider, w, r)
		if card == nil {
			return
		}
		// What the card said is kept for the entry; the photo is not.
		checkID, err := saveIDCheck(d, r.PathValue("id"), "", card)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not save the ID card check", err)
			return
		}

		httpx.JSON(w, http.StatusOK, map[string]any{
			"fields": card,
			// Named by the entry: the date of birth the age group is
			// decided by comes from here, not from the form.
			"checkId": checkID,
			// Answerable which service saw the document.
			"provider": provider.Name(),
		})
	}
}
