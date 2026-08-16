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
	mux.HandleFunc("POST /api/v1/auth/login", httpx.RateLimit(10, handleLogin(d)))
	mux.HandleFunc("POST /api/v1/auth/forgot-password", httpx.RateLimit(3, handleForgotPassword(d, mailCfg, sender)))
	mux.HandleFunc("POST /api/v1/auth/reset-password", httpx.RateLimit(10, handleResetPassword(d)))
	mux.HandleFunc("POST /api/v1/auth/logout", handleLogout(d))
	mux.HandleFunc("GET /api/v1/auth/me", handleMe(d))
	mux.HandleFunc("PATCH /api/v1/auth/me", handleUpdateMe(d))

	mountUserAccounts(mux, d)
	mountPuzzles(mux, d)
	for _, rs := range Registry() {
		rs.Mount(mux, d)
	}
	return mux
}
