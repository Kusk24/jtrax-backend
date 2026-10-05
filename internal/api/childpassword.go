// A parent setting a new password for their own child.
//
// A child who signs in with a username has no mailbox, so "forgot password"
// can only reach them through their parent's email. The parent portal lets
// the parent set it directly instead, from the child's profile. Only for a
// username login: a child with their own email address manages their own
// password, and a parent quietly changing it is not this feature.
package api

import (
	"database/sql"
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/Kusk24/jtrax-backend/internal/auth"
	"github.com/Kusk24/jtrax-backend/internal/httpx"
	"github.com/Kusk24/jtrax-backend/internal/mail"
)

func handleChildPassword(d *sql.DB, sender mail.Sender) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := requireIdentity(d, w, r)
		if id == nil {
			return
		}
		if id.Role != "Parent" {
			httpx.Error(w, http.StatusForbidden, "only a parent may reset their child's password", nil)
			return
		}
		var in struct {
			Password string `json:"password"`
		}
		if err := httpx.Decode(r, &in); err != nil {
			httpx.Error(w, http.StatusBadRequest, "invalid body", nil)
			return
		}
		// The family filter is part of the lookup: somebody else's child
		// reads as missing.
		var accountID, login, childName string
		err := d.QueryRow(`
			SELECT u.user_account_id, u.email, COALESCE(s.name, '')
			  FROM student s
			  JOIN student_parent sp ON sp.student_id = s.student_id
			  JOIN user_account u ON u.user_account_id = s.user_account_id
			 WHERE sp.parent_id = ? AND s.student_id = ?`,
			id.ParentID, r.PathValue("id")).Scan(&accountID, &login, &childName)
		if errors.Is(err, sql.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, "no such child", nil)
			return
		}
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not reset the password", err)
			return
		}
		if auth.LooksLikeEmail(login) {
			httpx.Error(w, http.StatusConflict,
				"this child signs in with their own email address — they can reset it with Forgot password", nil)
			return
		}
		if err := auth.ValidatePassword(in.Password); err != nil {
			httpx.Error(w, http.StatusBadRequest, err.Error(), nil)
			return
		}
		if err := auth.SetPassword(d, accountID, in.Password); err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not reset the password", err)
			return
		}
		go tellParentPasswordChanged(d, sender, id.UserAccountID, childName, login)
		w.WriteHeader(http.StatusNoContent)
	}
}

// tellParentPasswordChanged emails the parent that it was done — so a change
// they did not make, from a borrowed phone, does not go unnoticed.
func tellParentPasswordChanged(d *sql.DB, sender mail.Sender, parentAccountID, childName, login string) {
	if sender == nil {
		return
	}
	var email, name string
	if err := d.QueryRow(`SELECT email, COALESCE(display_name, '') FROM user_account WHERE user_account_id = ?`,
		parentAccountID).Scan(&email, &name); err != nil || !auth.LooksLikeEmail(email) {
		return
	}
	if strings.TrimSpace(name) == "" {
		name = "Parent"
	}
	e := mail.Email{
		Heading:  childName + "'s Password Was Changed",
		Greeting: "Dear " + name + ",",
		Paragraphs: []string{
			"The password for " + childName + "'s JTrax student account was changed from your Parent Portal.",
			childName + " can now sign in with the new password. They have been signed out on every device.",
		},
		Details: []mail.Detail{{Label: childName + "'s login username", Value: login}},
		After: []string{
			"If you did not make this change, please contact JCA Chess School straight away.",
		},
		Signoff: []string{"Best regards,", "JCA Chess School", "JTrax Account Support"},
	}
	if err := mail.Deliver(sender, email, childName+"'s JTrax Password Was Changed", e); err != nil {
		log.Printf("child password: notice to the parent failed: %v", err)
	}
}

// handleChildLogin tells a parent how their child signs in: the username, and
// whether it is the child's own email (then the parent cannot reset it).
func handleChildLogin(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := requireIdentity(d, w, r)
		if id == nil {
			return
		}
		if id.Role != "Parent" {
			httpx.Error(w, http.StatusForbidden, "parents only", nil)
			return
		}
		var login string
		err := d.QueryRow(`
			SELECT u.email FROM student s
			  JOIN student_parent sp ON sp.student_id = s.student_id
			  JOIN user_account u ON u.user_account_id = s.user_account_id
			 WHERE sp.parent_id = ? AND s.student_id = ?`,
			id.ParentID, r.PathValue("id")).Scan(&login)
		if errors.Is(err, sql.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, "no such child", nil)
			return
		}
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not load the login", err)
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"login": login, "ownEmail": auth.LooksLikeEmail(login)})
	}
}

