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
		// A child signs in with an ID — `stu_penny_ward` — which is not an
		// address and never will be. Matching it here and then handing it to
		// the SMTP sender would fail one layer down, log a real identifier as
		// a bounce, and still leave the child locked out. There is no reset
		// link that can reach them; the office changes the password instead.
		// The reply is the same as for an unknown address, so this does not
		// become a way to ask which identifiers are children's.
		if !auth.LooksLikeEmail(email) {
			// A child's login ID. Their link goes to their guardians'
			// inboxes instead — a grown-up is in the loop, and the child
			// still gets back in without calling the office.
			sendChildReset(d, cfg, sender, email)
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
		reset := mail.Email{
			Heading:    "Reset your password",
			Greeting:   "Hello " + displayName + ",",
			Paragraphs: []string{"Someone asked to reset your JTrax password. Use the button below to choose a new one."},
			Button:     &mail.Button{Label: "Choose a new password", URL: link},
			Note:       "The link works once and expires in an hour. If this wasn't you, ignore this email: your password stays as it is.",
		}
		if err := mail.Deliver(sender, email, "Reset your JTrax password", reset); err != nil {
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

// sendChildReset emails a reset link for a student's account to each of their
// guardians who has an email address. Unknown IDs, and children with no
// guardian to write to, get nothing — and the caller has already had the same
// neutral reply as everyone else.
func sendChildReset(d *sql.DB, cfg mail.Config, sender mail.Sender, loginID string) {
	var accountID, childName string
	if err := d.QueryRow(`
		SELECT u.user_account_id, COALESCE(s.name, u.display_name) FROM user_account u
		  JOIN student s ON s.user_account_id = u.user_account_id
		 WHERE lower(trim(u.email)) = ? AND u.role = 'Student'`, loginID).Scan(&accountID, &childName); err != nil {
		return
	}
	rows, err := d.Query(`
		SELECT DISTINCT g.email, COALESCE(g.display_name, p.name) FROM student s
		  JOIN student_parent sp ON sp.student_id = s.student_id
		  JOIN parent p ON p.parent_id = sp.parent_id
		  JOIN user_account g ON g.user_account_id = p.user_account_id
		 WHERE s.user_account_id = ?`, accountID)
	if err != nil {
		return
	}
	type guardian struct{ email, name string }
	var to []guardian
	for rows.Next() {
		var g guardian
		if rows.Scan(&g.email, &g.name) == nil && auth.LooksLikeEmail(g.email) {
			to = append(to, g)
		}
	}
	rows.Close()
	if len(to) == 0 {
		return
	}

	token, err := auth.CreateReset(d, accountID)
	if err != nil {
		log.Printf("password reset: could not create token for a student: %v", err)
		return
	}
	link := resetLink(cfg.PortalFor("Student"), token)
	if sender == nil {
		log.Printf("password reset: SMTP not configured — student link (SENSITIVE): %s", link)
		return
	}
	for _, g := range to {
		e := mail.Email{
			Heading:  "Reset " + childName + "'s password",
			Greeting: "Hello " + g.name + ",",
			Paragraphs: []string{
				childName + " asked to reset their JTrax student password. Use the button below to choose a new one together.",
			},
			Details: []mail.Detail{{Label: childName + "'s login ID", Value: loginID}},
			Button:  &mail.Button{Label: "Choose a new password for " + childName, URL: link},
			Note: "The link works once and expires in an hour. If " + childName +
				" didn't ask for this, ignore this email: the password stays as it is.",
		}
		if err := mail.Deliver(sender, g.email, "Reset "+childName+"'s JTrax password", e); err != nil {
			log.Printf("password reset: send to a guardian failed: %v", err)
		}
	}
}

// handleChangePassword lets a signed-in person replace their own password.
func handleChangePassword(d *sql.DB) http.HandlerFunc {
	// Per account, so the current-password check cannot be used to guess it.
	limiter := httpx.NewLimiter(5)
	return func(w http.ResponseWriter, r *http.Request) {
		id := requireIdentity(d, w, r)
		if id == nil {
			return
		}
		var in struct {
			CurrentPassword string `json:"currentPassword"`
			NewPassword     string `json:"newPassword"`
		}
		if err := httpx.Decode(r, &in); err != nil || in.CurrentPassword == "" || in.NewPassword == "" {
			httpx.Error(w, http.StatusBadRequest, "current and new password are required", err)
			return
		}
		if !limiter.Allow(id.UserAccountID) {
			httpx.Error(w, http.StatusTooManyRequests, "too many attempts — wait a minute and try again", nil)
			return
		}
		if err := auth.ValidatePassword(in.NewPassword); err != nil {
			httpx.Error(w, http.StatusBadRequest, err.Error(), nil)
			return
		}
		if err := auth.ChangePassword(d, id.UserAccountID, in.CurrentPassword, in.NewPassword, bearerToken(r)); err != nil {
			if errors.Is(err, auth.ErrWrongPassword) {
				httpx.Error(w, http.StatusBadRequest, "your current password is not right", nil)
				return
			}
			httpx.Error(w, http.StatusInternalServerError, "could not change the password", err)
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]string{"status": "password changed"})
	}
}
