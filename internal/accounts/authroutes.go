// Authentication and account lifecycle routes, including their existing rate limits.
package accounts

import (
	"database/sql"

	"github.com/gin-gonic/gin"

	"github.com/Kusk24/jtrax-backend/internal/httpx"
	"github.com/Kusk24/jtrax-backend/internal/mail"
)

func MountAuth(mux gin.IRoutes, d *sql.DB, mailCfg mail.Config, sender mail.Sender) {
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
	mux.POST("/api/v1/auth/login", httpx.RateLimit(120), handleLogin(d))
	// Forgot-password keeps a per-address budget for the same reason, since
	// each accepted call sends mail to somebody else's inbox.
	mux.POST("/api/v1/auth/forgot-password", httpx.RateLimit(60), handleForgotPassword(d, mailCfg, sender))
	mux.POST("/api/v1/auth/reset-password", httpx.RateLimit(10), handleResetPassword(d))
	mux.POST("/api/v1/auth/change-password", httpx.RateLimit(30), handleChangePassword(d))
	mux.POST("/api/v1/students/:id/password", httpx.RateLimit(20), handleChildPassword(d, sender))
	mux.GET("/api/v1/students/:id/login", handleChildLogin(d))
	// Staff only: a new parent chooses their own password from this link.
	mux.POST("/api/v1/user-accounts/:id/invite", httpx.RateLimit(60), handleInvite(d, mailCfg, sender))
	mux.POST("/api/v1/auth/logout", handleLogout(d))
	mux.GET("/api/v1/auth/me", handleMe(d))
	mux.PATCH("/api/v1/auth/me", handleUpdateMe(d))

}
