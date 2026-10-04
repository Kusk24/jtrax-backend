// Inviting a family to set their own password.
//
// The office used to create a parent's login with a temporary password and read
// it out or pass it on — a credential in a third person's hands, and one most
// families never changed. Now the office creates the account and this sends
// the parent a link to choose their own. It is the forgot-password link with a
// week's lifetime and a welcome instead of "someone asked to reset".
package api

import (
	"database/sql"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/Kusk24/jtrax-backend/internal/auth"
	"github.com/Kusk24/jtrax-backend/internal/httpx"
	"github.com/Kusk24/jtrax-backend/internal/mail"
)

func handleInvite(d *sql.DB, cfg mail.Config, sender mail.Sender) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := requireIdentity(d, w, r)
		if id == nil {
			return
		}
		if !isStaff(id.Role) {
			httpx.Error(w, http.StatusForbidden, "not allowed", nil)
			return
		}
		// Optionally, the logins of children registered with this parent a
		// moment ago, so the family gets everything in one email. Nothing
		// here is stored; each is checked against the child's real account
		// before it is written into an email.
		var in struct {
			StudentLogins []studentLogin `json:"studentLogins"`
		}
		if err := httpx.Decode(r, &in); err != nil && !errors.Is(err, io.EOF) {
			httpx.Error(w, http.StatusBadRequest, "invalid body", err)
			return
		}
		accountID := r.PathValue("id")
		var email, displayName, role string
		if err := d.QueryRow(`SELECT email, display_name, role FROM user_account WHERE user_account_id = ?`,
			accountID).Scan(&email, &displayName, &role); err != nil {
			httpx.Error(w, http.StatusNotFound, "not found", err)
			return
		}
		// A child's login ID is not a mailbox; the office sets their password.
		if !auth.LooksLikeEmail(email) {
			httpx.Error(w, http.StatusUnprocessableEntity, "this account has no email address to send an invite to", nil)
			return
		}

		for _, l := range in.StudentLogins {
			if !isChildLogin(d, accountID, l) {
				httpx.Error(w, http.StatusUnprocessableEntity, "that student login does not belong to this parent's child", nil)
				return
			}
		}

		token, err := auth.CreateResetFor(d, accountID, auth.InviteTTL)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not create the invite", err)
			return
		}
		link := resetLink(cfg.PortalFor(role), token)

		if sender == nil {
			// No SMTP here — local development. The link is logged so the
			// account can still be set up, and the console is told nothing
			// was delivered.
			log.Printf("invite: SMTP not configured — link for %s (SENSITIVE): %s", email, link)
			httpx.JSON(w, http.StatusAccepted, map[string]any{"email": email, "delivered": false})
			return
		}
		subject, body := "Welcome to JCA Chess School: set your password",
			inviteEmail(displayName, email, childNamesOf(d, accountID), link, in.StudentLogins)
		if role == "Student" {
			// An older student with their own address: the parent's welcome
			// would tell them about "your child's" classes.
			subject, body = "JCA Chess School: set your password", studentPasswordEmail(displayName, email, link)
		}
		if err := mail.Deliver(sender, email, subject, body); err != nil {
			httpx.Error(w, http.StatusBadGateway, "the invite email could not be sent", err)
			return
		}
		httpx.JSON(w, http.StatusAccepted, map[string]any{"email": email, "delivered": true})
	}
}

// childNamesOf names a parent account's children, for the welcome line.
func childNamesOf(d *sql.DB, accountID string) []string {
	rows, err := d.Query(`
		SELECT s.name FROM parent p
		  JOIN student_parent sp ON sp.parent_id = p.parent_id
		  JOIN student s ON s.student_id = sp.student_id
		 WHERE p.user_account_id = ? ORDER BY s.name`, accountID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		if rows.Scan(&n) == nil && n != "" {
			names = append(names, n)
		}
	}
	return names
}

// studentLogin is a child's sign-in for the student app, sent to their parent.
type studentLogin struct {
	Name     string `json:"name"`
	LoginID  string `json:"loginId"`
	Password string `json:"password"`
}

// isChildLogin checks a login really is this parent's child's, and that the
// password is the one on the account — so the email never carries a wrong
// password, or a login for somebody else's child.
func isChildLogin(d *sql.DB, parentAccountID string, l studentLogin) bool {
	var hash string
	err := d.QueryRow(`
		SELECT u.password_hash FROM user_account u
		  JOIN student s ON s.user_account_id = u.user_account_id
		  JOIN student_parent sp ON sp.student_id = s.student_id
		  JOIN parent p ON p.parent_id = sp.parent_id
		 WHERE p.user_account_id = ? AND u.email = ? AND u.role = 'Student'`,
		parentAccountID, l.LoginID).Scan(&hash)
	return err == nil && auth.VerifyPassword(l.Password, hash)
}

func inviteEmail(name, email string, children []string, link string, logins []studentLogin) mail.Email {
	about := "your child's"
	if len(children) > 0 {
		about = joinNames(children) + "'s"
	}
	details := []mail.Detail{{Label: "Your sign-in email", Value: email}}
	paragraphs := []string{
		"JCA Chess School has created your JTrax parent account. With it you can follow " +
			about + " classes, credits and practice, see announcements, and message the office.",
		"Choose your password to get started.",
	}
	if len(logins) > 0 {
		names := make([]string, len(logins))
		for i, l := range logins {
			names[i] = l.Name
			details = append(details,
				mail.Detail{Label: l.Name + "'s login ID", Value: l.LoginID},
				mail.Detail{Label: l.Name + "'s password", Value: l.Password})
		}
		paragraphs = append(paragraphs, joinNames(names)+" can sign in to the JTrax student app with the login below. "+
			"Keep it somewhere safe. The office can reset it if it is lost.")
	}
	return mail.Email{
		Heading:    "Welcome to JCA Chess School",
		Greeting:   "Hello " + name + ",",
		Paragraphs: paragraphs,
		Details:    details,
		Button:     &mail.Button{Label: "Set your password", URL: link},
		Note: "The link works once and expires in 7 days. If it has expired, ask the office to send a new one. " +
			"If you weren't expecting this email, you can ignore it.",
	}
}

// studentPasswordEmail is the link sent to a student who signs in with their
// own email address, to choose a new password.
func studentPasswordEmail(name, email, link string) mail.Email {
	return mail.Email{
		Heading:  "Set your password",
		Greeting: "Hello " + name + ",",
		Paragraphs: []string{
			"JCA Chess School has sent you a link to choose a new password for your JTrax student account.",
			"เราได้ส่งลิงก์สำหรับตั้งรหัสผ่านใหม่ของบัญชีนักเรียน JTrax ของคุณ",
		},
		Details: []mail.Detail{{Label: "Your sign-in email", Value: email}},
		Button:  &mail.Button{Label: "Set your password", URL: link},
		Note: "The link works once and expires in 7 days. If it has expired, ask the office to send a new one. " +
			"If you weren't expecting this email, you can ignore it.",
	}
}

// joinNames reads "Uri", "Uri and Mini", "Uri, Mini and Penny".
func joinNames(names []string) string {
	if len(names) <= 1 {
		return strings.Join(names, "")
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}
