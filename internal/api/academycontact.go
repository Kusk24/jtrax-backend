// The academy's contact details, for the footer of the public pages.
//
// The registration form and the results page are the academy's public face:
// a link on a poster, forwarded to a grandparent. Like any site, they end with
// how to reach the people behind them. The details are set by an admin in
// Settings and kept in system_configuration under academy_*; only those keys
// are served here, so nothing else in that table becomes public.
package api

import (
	"database/sql"
	"net/http"

	"github.com/Kusk24/jtrax-backend/internal/httpx"
)

// academyContactKeys maps each served field to its configuration key.
var academyContactKeys = map[string]string{
	"name":     "academy_name",
	"phone":    "academy_phone",
	"email":    "academy_email",
	"lineId":   "academy_line_id",
	"facebook": "academy_facebook",
	"website":  "academy_website",
	"address":  "academy_address",
	"hours":    "academy_hours",
}

func handleAcademyContact(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		out := map[string]string{}
		for field, key := range academyContactKeys {
			var v string
			err := d.QueryRow(`SELECT config_value FROM system_configuration WHERE config_key = ?`, key).Scan(&v)
			if err != nil && err != sql.ErrNoRows {
				httpx.Error(w, http.StatusInternalServerError, "could not load contact details", err)
				return
			}
			if v != "" {
				out[field] = v
			}
		}
		httpx.JSON(w, http.StatusOK, out)
	}
}

func mountAcademyContact(mux *http.ServeMux, d *sql.DB) {
	mux.HandleFunc("GET /api/v1/public/academy", httpx.RateLimit(120, handleAcademyContact(d)))
}
