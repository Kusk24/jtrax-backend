// Forgot-password endpoints. Both are unauthenticated, so both are rate
// limited at the route table and neither reveals whether an account exists.
package api

import (
	"database/sql"
	"errors"
	"log"
	"net/http"
	"net/url"
	"strings"

	"github.com/Kusk24/jtrax-backend/internal/auth"
	"github.com/Kusk24/jtrax-backend/internal/httpx"
	"github.com/Kusk24/jtrax-backend/internal/mail"
)

func handleForgotPassword(d *sql.DB, cfg mail.Config, sender mail.Sender) http.HandlerFunc {
	// Budgets reset requests per address, for the same reason sign-in is
	// budgeted per account: the callers all arrive from one server, so an IP
	// budget of three a minute was three for the whole academy. Three per
	// address still stops anyone using the academy's mail reputation to pester
	// a third party, which is what this limit is actually for.
	resetLimiter := httpx.NewLimiter(3)

	return func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Email string `json:"email"`
		}
		if err := httpx.Decode(r, &in); err != nil {
			httpx.Error(w, http.StatusBadRequest, "email is required", err)
			return
		}
		email := auth.NormalizeEmail(in.Email)

		// The response is identical whether or not the address is registered.
		// Anything else turns this endpoint into a way to enumerate the
		// academy's parents and students by trying addresses.
		defer httpx.JSON(w, http.StatusAccepted, map[string]string{
			"status": "if that email has an account, a reset link is on its way",
		})
		// Over budget is treated exactly like an unknown address — the same
		// reply, nothing sent. Saying "too many requests" here would leak that
		// somebody has been asking about this particular address.
		if email == "" || !resetLimiter.Allow(email) {
			return
		}

		var accountID, displayName, role string
		err := d.QueryRow(`SELECT user_account_id, display_name, role FROM user_account
		                   WHERE lower(trim(email)) = ?`, email).
			Scan(&accountID, &displayName, &role)
		if err != nil {
			return // unknown address: say nothing, do nothing
		}

		token, err := auth.CreateReset(d, accountID)
		if err != nil {
			log.Printf("password reset: could not create token: %v", err)
			return
		}
		// The role picks the portal, so the link always lands on the app that
		// account can actually sign in to.
		link := resetLink(cfg.PortalFor(role), token)

		if sender == nil {
			// No SMTP configured. Printing the link keeps local development
			// usable; it is a credential in the log, so it says so loudly and
			// only ever happens when the deployment has no mail configured.
			log.Printf("password reset: SMTP not configured — link for %s (SENSITIVE): %s", email, link)
			return
		}
		body := "Hello " + displayName + ",\n\n" +
			"Someone asked to reset your JTrax password. Open the link below to choose a new one:\n\n" +
			link + "\n\nThe link works once and expires in an hour. " +
			"If this wasn't you, ignore this email — your password stays as it is.\n\nJCA Chess Academy\n"
		if err := sender.Send(email, "Reset your JTrax password", body); err != nil {
			// Logged, not returned: the caller already has the neutral reply,
			// and the error would tell them the address exists.
			log.Printf("password reset: send to a registered address failed: %v", err)
		}
	}
}

// resetLink builds the portal URL the user clicks. Falls back to a bare path
// when APP_URL is unset so the token is still usable in development.
func resetLink(appURL, token string) string {
	return strings.TrimSuffix(appURL, "/") + "/reset-password?token=" + url.QueryEscape(token)
}

func handleResetPassword(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Token    string `json:"token"`
			Password string `json:"password"`
		}
		if err := httpx.Decode(r, &in); err != nil || in.Token == "" {
			httpx.Error(w, http.StatusBadRequest, "token and password are required", err)
			return
		}
		if err := auth.ValidatePassword(in.Password); err != nil {
			httpx.Error(w, http.StatusBadRequest, err.Error(), nil)
			return
		}
		if err := auth.ConsumeReset(d, in.Token, in.Password); err != nil {
			if errors.Is(err, auth.ErrResetInvalid) {
				httpx.Error(w, http.StatusBadRequest, "that reset link is invalid or has expired", nil)
				return
			}
			httpx.Error(w, http.StatusInternalServerError, "could not reset the password", err)
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]string{"status": "password updated"})
	}
}
