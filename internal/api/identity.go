// Login, logout and current-user endpoints.
package api

import (
	"database/sql"
	"net/http"
	"strings"

	"github.com/Kusk24/jtrax-backend/internal/auth"
	"github.com/Kusk24/jtrax-backend/internal/httpx"
)

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	return ""
}

// requireIdentity resolves the caller or writes 401.
func requireIdentity(d *sql.DB, w http.ResponseWriter, r *http.Request) *auth.Identity {
	id, err := auth.Lookup(d, bearerToken(r))
	if err != nil {
		httpx.Error(w, http.StatusUnauthorized, "sign in required", nil)
		return nil
	}
	return id
}

func handleLogin(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Email    string `json:"email"`
			Password string `json:"password"`
		}
		if err := httpx.Decode(r, &in); err != nil || in.Email == "" || in.Password == "" {
			httpx.Error(w, http.StatusBadRequest, "email and password are required", err)
			return
		}
		var accountID, hash string
		err := d.QueryRow(`SELECT user_account_id, password_hash FROM user_account WHERE email = ?`,
			strings.ToLower(strings.TrimSpace(in.Email))).Scan(&accountID, &hash)
		if err != nil || !auth.VerifyPassword(in.Password, hash) {
			// Same message for unknown email and wrong password.
			httpx.Error(w, http.StatusUnauthorized, "invalid email or password", nil)
			return
		}
		token, err := auth.CreateSession(d, accountID)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not create session", err)
			return
		}
		id, err := auth.Lookup(d, token)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not load account", err)
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"token": token, "user": id})
	}
}

func handleLogout(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := auth.DeleteSession(d, bearerToken(r)); err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not sign out", err)
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]string{"status": "signed out"})
	}
}

func handleMe(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := requireIdentity(d, w, r)
		if id == nil {
			return
		}
		httpx.JSON(w, http.StatusOK, id)
	}
}

// handleUpdateMe lets any signed-in user change their own preferences.
func handleUpdateMe(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := requireIdentity(d, w, r)
		if id == nil {
			return
		}
		var in struct {
			DisplayName *string `json:"displayName"`
			Language    *string `json:"languagePreference"`
			Theme       *string `json:"themePreference"`
		}
		if err := httpx.Decode(r, &in); err != nil {
			httpx.Error(w, http.StatusBadRequest, "invalid body", err)
			return
		}
		if in.Language != nil && *in.Language != "EN" && *in.Language != "TH" {
			httpx.Error(w, http.StatusBadRequest, "languagePreference must be EN or TH", nil)
			return
		}
		if in.Theme != nil && *in.Theme != "Light" && *in.Theme != "Dark" && *in.Theme != "System" {
			httpx.Error(w, http.StatusBadRequest, "themePreference must be Light, Dark or System", nil)
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
		fresh, _ := auth.Lookup(d, bearerToken(r))
		httpx.JSON(w, http.StatusOK, fresh)
	}
}
