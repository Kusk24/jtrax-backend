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
		subject, body := "Welcome to JCA Chess School! ♟️",
			inviteEmail(displayName, email, childNamesOf(d, accountID), link, cfg.PortalFor(role), in.StudentLogins)
		if role == "Student" {
			// An older student with their own address: the parent's welcome
			// would tell them about "your child's" classes.
			subject, body = "JCA Chess School: Set Your JTrax Password", studentPasswordEmail(displayName, email, link)
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

// inviteEmail is the parent's welcome: who they are signed in as, their
// children's student logins, how to set a password, and — for a child with a
// username rather than an email — where in the portal to reset theirs.
func inviteEmail(name, email string, children []string, link, portal string, logins []studentLogin) mail.Email {
	kids := joinNames(children)
	welcome := "Welcome to JCA Chess School! We're happy to have you with us."
	about, journey := "your child's", "your child's"
	if kids != "" {
		welcome = "Welcome to JCA Chess School! We're happy to have you and " + kids + " with us."
		about, journey = kids+"'s", kids+"'s"
	}
	paragraphs := []string{
		welcome,
		"Your JTrax parent account has been created. Through JTrax, you can easily keep track of " + about +
			" classes, credits, practice activities, announcements, and messages from the school.",
		"To get started, please set a password for your parent account using the button below.",
	}
	details := []mail.Detail{{Label: "Parent Account"}, {Label: "Your sign-in email", Value: email}}

	/* The children who sign in with a username: their password is the
	   parent's to reset, in the portal. One with their own email resets it
	   themselves. */
	var usernames []string
	if len(logins) > 0 {
		names := make([]string, len(logins))
		heading := "Student Login"
		if len(logins) > 1 {
			heading = "Student Logins"
		}
		details = append(details, mail.Detail{Label: heading})
		for i, l := range logins {
			names[i] = l.Name
			idLabel, pwLabel := "Login ID", "Password"
			if len(logins) > 1 {
				idLabel, pwLabel = l.Name+"'s login ID", l.Name+"'s password"
			}
			details = append(details,
				mail.Detail{Label: idLabel, Value: l.LoginID},
				mail.Detail{Label: pwLabel, Value: l.Password})
			if !strings.Contains(l.LoginID, "@") {
				usernames = append(usernames, l.Name)
			}
		}
		paragraphs = append(paragraphs, joinNames(names)+
			" can also sign in to the JTrax student app using the student login details below. "+
			"Please keep these login details safe.")
	}

	var after []string
	if len(usernames) > 0 {
		card := usernames[0] + "'s card"
		if len(usernames) > 1 {
			card = "your child's card"
		}
		after = append(after,
			"If "+joinNames(usernames)+" forgets the password, you can set a new one in the JTrax parent portal:",
			"- Open JTrax and sign in",
			"- On Home, tap "+card,
			"- Scroll down to Account & Security",
			"- Tap Reset Password",
			"The school office can also help reset it.")
	}
	after = append(after,
		"We look forward to supporting "+journey+" chess journey with JCA Chess School!",
		"ยินดีต้อนรับสู่ JCA Chess School กรุณาตั้งรหัสผ่านบัญชีผู้ปกครอง JTrax ของคุณด้วยปุ่มด้านบน")

	return mail.Email{
		Heading:    "Welcome to JCA Chess School",
		Greeting:   "Hello " + name + ",",
		Paragraphs: paragraphs,
		Details:    details,
		Button:     &mail.Button{Label: "Set Your Password", URL: link},
		Second:     &mail.Button{Label: "Open JTrax", URL: strings.TrimSuffix(portal, "/") + "/"},
		After:      after,
		Note: "This password setup link is valid for 7 days and can only be used once. If it has expired, " +
			"ask the school office to send a new one. If you weren't expecting this email, you can ignore it.",
		Signoff: []string{"Best regards,", "JCA Chess School", "JTrax Parent Portal"},
	}
}

// studentPasswordEmail is the link sent to a student who signs in with their
// own email address, to choose a new password.
func studentPasswordEmail(name, email, link string) mail.Email {
	return mail.Email{
		Heading:  "Set Your JTrax Password",
		Greeting: "Dear " + name + ",",
		Paragraphs: []string{
			"Your JTrax student account has been created by JCA Chess School.",
			"To access your student account, please use the button below to set your password. " +
				"Once your password has been set, you can use your JTrax account to sign in to the " +
				"JTrax Student Portal and access your account information.",
		},
		Button: &mail.Button{Label: "Set My Password", URL: link},
		After: []string{
			"This password setup link is valid for 7 days and can only be used to set your password once.",
			"If you did not expect to receive this email or believe this account was created in error, " +
				"please contact JCA Chess School for assistance.",
			"อีเมลฉบับนี้ส่งจาก JCA Chess School เพื่อให้คุณตั้งรหัสผ่านสำหรับบัญชี JTrax ของคุณ",
		},
		Signoff: []string{"Best regards,", "JCA Chess School", "JTrax Student Portal"},
	}
}

// joinNames reads "Uri", "Uri and Mini", "Uri, Mini and Penny".
func joinNames(names []string) string {
	if len(names) <= 1 {
		return strings.Join(names, "")
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}
