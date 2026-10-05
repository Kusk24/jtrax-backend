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
	"strings"

	"github.com/Kusk24/jtrax-backend/internal/httpx"
	"github.com/Kusk24/jtrax-backend/internal/mail"
)

// academyContactKeys maps each served field to its configuration key.
var academyContactKeys = map[string]string{
	"name":      "academy_name",
	"phone":     "academy_phone",
	"email":     "academy_email",
	"lineId":    "academy_line_id",
	"facebook":  "academy_facebook",
	"instagram": "academy_instagram",
	"website":   "academy_website",
	"address":   "academy_address",
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

// AcademyContact is what the office saved in Settings → Academy Contact, for
// the footer of every email and the payment pages (mail.SetContactSource).
// An empty field falls back to the website's details in mail.CurrentContact.
func AcademyContact(d *sql.DB) mail.Contact {
	get := func(key string) string {
		var v string
		d.QueryRow(`SELECT config_value FROM system_configuration WHERE config_key = ?`, key).Scan(&v)
		return strings.TrimSpace(v)
	}
	return mail.Contact{
		Phone:   get("academy_phone"),
		Email:   get("academy_email"),
		LINE:    lineLink(get("academy_line_id")),
		Address: get("academy_address"),
	}
}

// lineLink turns what the office typed for LINE into a link: a link as it
// is, "lin.ee/…" with its scheme, and an "@id" as LINE's add-friend link.
func lineLink(v string) string {
	switch {
	case v == "":
		return ""
	case strings.HasPrefix(v, "http://") || strings.HasPrefix(v, "https://"):
		return v
	case strings.Contains(v, "/"):
		return "https://" + v
	}
	return "https://line.me/R/ti/p/@" + strings.TrimPrefix(v, "@")
}
