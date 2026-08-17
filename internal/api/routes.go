// Route table: auth endpoints plus every registry resource under /api/v1.
package api

import (
	"database/sql"
	"net/http"

	"github.com/Kusk24/jtrax-backend/internal/httpx"
	"github.com/Kusk24/jtrax-backend/internal/mail"
)

// NewHandler builds the full API handler (CORS applied by the caller), reading
// the mail configuration from the environment.
func NewHandler(d *sql.DB) http.Handler {
	cfg := mail.FromEnv()
	return NewHandlerWithMail(d, cfg, mail.New(cfg))
}

// NewHandlerWithMail is the injectable form: tests pass a Sender that captures
// messages instead of delivering them.
func NewHandlerWithMail(d *sql.DB, mailCfg mail.Config, sender mail.Sender) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		httpx.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	// Every unauthenticated endpoint is rate-limited. Forgot-password gets the
	// tightest budget of the three: each accepted call sends an email to
	// someone else's inbox, so an unthrottled one is a way to use the academy's
	// mail reputation to spam a third party.
	// Sign-in is budgeted **per account**, inside the handler, not per IP.
	//
	// The portals call this API from their own servers, so every member of
	// staff arrives from one address and an IP budget of ten a minute was ten
	// tries for the whole academy — the eleventh person to sign in that minute
	// was refused, and the console showed it as a wrong password. The remaining
	// IP limit here is a flood guard set well above any real burst, not a
	// per-person budget.
	mux.HandleFunc("POST /api/v1/auth/login", httpx.RateLimit(120, handleLogin(d)))
	// Forgot-password keeps a per-address budget for the same reason, since
	// each accepted call sends mail to somebody else's inbox.
	mux.HandleFunc("POST /api/v1/auth/forgot-password", httpx.RateLimit(60, handleForgotPassword(d, mailCfg, sender)))
	mux.HandleFunc("POST /api/v1/auth/reset-password", httpx.RateLimit(10, handleResetPassword(d)))
	mux.HandleFunc("POST /api/v1/auth/logout", handleLogout(d))
	mux.HandleFunc("GET /api/v1/auth/me", handleMe(d))
	mux.HandleFunc("PATCH /api/v1/auth/me", handleUpdateMe(d))

	mountUserAccounts(mux, d)
	mountGameRooms(mux, d)
	mountPuzzles(mux, d)
	mountLine(mux, d)
	mountLichess(mux, d)
	// Before the registry: `/students/{id}/cascade` is a more specific pattern
	// than `/students/{id}`, so the two coexist either way, but keeping the
	// bespoke mounts together says which is which.
	mountPeopleCascade(mux, d)
	for _, rs := range Registry() {
		rs.Mount(mux, d)
	}
	return mux
}
