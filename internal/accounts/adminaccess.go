// Access checks shared by school workflows.
package accounts

import (
	"database/sql"
	"net/http"

	"github.com/Kusk24/jtrax-backend/internal/auth"
	"github.com/Kusk24/jtrax-backend/internal/httpx"
)

func RequireAdmin(d *sql.DB, w http.ResponseWriter, r *http.Request) *auth.Identity {
	id := RequireIdentity(d, w, r)
	if id == nil {
		return nil
	}
	if id.Role != "Admin" {
		httpx.Error(w, http.StatusForbidden, "not allowed", nil)
		return nil
	}
	return id
}
