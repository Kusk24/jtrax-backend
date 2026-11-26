// Access checks shared by school workflows.
package accounts

import (
	"database/sql"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/Kusk24/jtrax-backend/internal/auth"
	"github.com/Kusk24/jtrax-backend/internal/httpx"
)

func RequireAdmin(d *sql.DB, ctx *gin.Context) *auth.Identity {
	id := RequireIdentity(d, ctx)
	if id == nil {
		return nil
	}
	if id.Role != "Admin" {
		httpx.Error(ctx, http.StatusForbidden, "not allowed", nil)
		return nil
	}
	return id
}
