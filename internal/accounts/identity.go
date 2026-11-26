// Login, logout and current-user endpoints.
package accounts

import (
	"database/sql"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/Kusk24/jtrax-backend/internal/auth"
	"github.com/Kusk24/jtrax-backend/internal/httpx"
)

func BearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	return ""
}

// RequireIdentity resolves the caller or writes 401.
func RequireIdentity(d *sql.DB, ctx *gin.Context) *auth.Identity {
	id, err := auth.Lookup(d, BearerToken(ctx.Request))
	if err != nil {
		httpx.Error(ctx, http.StatusUnauthorized, "sign in required", nil)
		return nil
	}
	return id
}

func handleLogin(d *sql.DB) gin.HandlerFunc {
	// Budgets sign-in attempts **per account**, not per IP.
	//
	// The console reaches this API from its own server, so every member of
	// staff shares one address and an IP budget would let the first few
	// attempts of the minute lock out everybody else. Ten tries per account
	// per minute stops password guessing without taking a colleague down.
	//
	// Owned by the handler rather than the package, so two servers in one
	// process — which is every test in this suite — do not share a budget.
	loginLimiter := httpx.NewLimiter(10)

	return func(ctx *gin.Context) {
		var in struct {
			Email    string `json:"email"`
			Password string `json:"password"`
		}
		if err := httpx.Decode(ctx, &in); err != nil || in.Email == "" || in.Password == "" {
			httpx.Error(ctx, http.StatusBadRequest, "email and password are required", err)
			return
		}
		email := auth.NormalizeEmail(in.Email)
		if !loginLimiter.Allow(email) {
			// A distinct status, because "you are locked out for a minute" and
			// "your password is wrong" send a person to different places, and
			// the portals show different messages for each.
			httpx.Error(ctx, http.StatusTooManyRequests,
				"too many sign-in attempts for this account, try again in a minute", nil)
			return
		}
		var accountID, hash string
		// Matched case-insensitively as well as normalised, so a row that
		// predates normalisation is still reachable.
		err := d.QueryRow(`SELECT user_account_id, password_hash FROM user_account
		                   WHERE lower(trim(email)) = ?`, email).Scan(&accountID, &hash)
		if err != nil || !auth.VerifyPassword(in.Password, hash) {
			// Same message for unknown email and wrong password.
			httpx.Error(ctx, http.StatusUnauthorized, "invalid email or password", nil)
			return
		}
		// A correct password clears the budget, so earlier fumbles are not
		// still counted against them for the rest of the minute.
		loginLimiter.Forget(email)
		if auth.IsArchived(d, accountID) {
			httpx.Error(ctx, http.StatusForbidden, auth.ErrArchived.Error(), nil)
			return
		}
		token, err := auth.CreateSession(d, accountID)
		if err != nil {
			httpx.Error(ctx, http.StatusInternalServerError, "could not create session", err)
			return
		}
		id, err := auth.Lookup(d, token)
		if err != nil {
			httpx.Error(ctx, http.StatusInternalServerError, "could not load account", err)
			return
		}
		httpx.JSON(ctx, http.StatusOK, map[string]any{"token": token, "user": id})
	}
}

func handleLogout(d *sql.DB) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		if err := auth.DeleteSession(d, BearerToken(ctx.Request)); err != nil {
			httpx.Error(ctx, http.StatusInternalServerError, "could not sign out", err)
			return
		}
		httpx.JSON(ctx, http.StatusOK, map[string]string{"status": "signed out"})
	}
}

func handleMe(d *sql.DB) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		id := RequireIdentity(d, ctx)
		if id == nil {
			return
		}
		httpx.JSON(ctx, http.StatusOK, id)
	}
}

// handleUpdateMe lets any signed-in user change their own preferences.
func handleUpdateMe(d *sql.DB) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		id := RequireIdentity(d, ctx)
		if id == nil {
			return
		}
		var in struct {
			DisplayName *string `json:"displayName"`
			Language    *string `json:"languagePreference"`
			Theme       *string `json:"themePreference"`
		}
		if err := httpx.Decode(ctx, &in); err != nil {
			httpx.Error(ctx, http.StatusBadRequest, "invalid body", err)
			return
		}
		// A parent's name is the academy's record of them, changed by the
		// office: their profile in the portal is read-only. A student still
		// names themselves in the student panel.
		if in.DisplayName != nil && id.Role == "Parent" {
			httpx.Error(ctx, http.StatusForbidden, "a parent's name is changed by the academy", nil)
			return
		}
		if in.Language != nil && *in.Language != "EN" && *in.Language != "TH" {
			httpx.Error(ctx, http.StatusBadRequest, "languagePreference must be EN or TH", nil)
			return
		}
		if in.Theme != nil && *in.Theme != "Light" && *in.Theme != "Dark" && *in.Theme != "System" {
			httpx.Error(ctx, http.StatusBadRequest, "themePreference must be Light, Dark or System", nil)
			return
		}
		set := func(col string, v *string) {
			if v != nil {
				d.Exec(`UPDATE user_account SET `+col+` = ? WHERE user_account_id = ?`, *v, id.UserAccountID)
			}
		}
		set("display_name", in.DisplayName)
		set("language_preference", in.Language)
		set("theme_preference", in.Theme)
		fresh, _ := auth.Lookup(d, BearerToken(ctx.Request))
		httpx.JSON(ctx, http.StatusOK, fresh)
	}
}
