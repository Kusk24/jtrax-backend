// Route table: auth endpoints plus every registry resource under /api/v1.
package api

import (
	"database/sql"
	"net/http"

	"github.com/Kusk24/jtrax-backend/internal/httpx"
)

// NewHandler builds the full API handler (CORS applied by the caller).
func NewHandler(d *sql.DB) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		httpx.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	// Login is the only unauthenticated endpoint, so it is rate-limited.
	mux.HandleFunc("POST /api/v1/auth/login", httpx.RateLimit(10, handleLogin(d)))
	mux.HandleFunc("POST /api/v1/auth/logout", handleLogout(d))
	mux.HandleFunc("GET /api/v1/auth/me", handleMe(d))
	mux.HandleFunc("PATCH /api/v1/auth/me", handleUpdateMe(d))

	mountUserAccounts(mux, d)
	for _, rs := range Registry() {
		rs.Mount(mux, d)
	}
	return mux
}
