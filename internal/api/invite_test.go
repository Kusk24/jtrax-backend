// The office invites a parent to choose their own password.
package api_test

import (
	"strings"
	"testing"

	"github.com/Kusk24/jtrax-backend/internal/db"
)

func TestTheOfficeInvitesAParentToSetTheirPassword(t *testing.T) {
	srv, cap := newResetServer(t)
	admin := &client{t: t, srv: srv}
	admin.login("admin@jca.ac.th")

	status, got, _ := admin.do("POST", "/api/v1/user-accounts/usr_sandy/invite", nil)
	if status != 202 || got["delivered"] != true {
		t.Fatalf("invite: %d (%v)", status, got)
	}
	to, body := cap.last()
	if to != "sandy01234@gmail.com" {
		t.Fatalf("sent to %q", to)
	}
	for _, want := range []string{"Hello Sandy Jones", "Penny", "Your sign-in email: sandy01234@gmail.com", "expires in 7 days", "/reset-password?token="} {
		if !strings.Contains(body, want) {
			t.Errorf("the invite does not say %q:\n%s", want, body)
		}
	}

	// The link is a real one: it sets the password, once.
	anon := &client{t: t, srv: srv}
	if status, _, _ := anon.do("POST", "/api/v1/auth/reset-password", map[string]any{
		"token": tokenFrom(body), "password": "Chosen-by-Sandy-1",
	}); status != 200 {
		t.Fatalf("setting the password from the invite: %d", status)
	}
	if status, _, _ := anon.do("POST", "/api/v1/auth/login", map[string]string{
		"email": "sandy01234@gmail.com", "password": "Chosen-by-Sandy-1",
	}); status != 200 {
		t.Fatalf("signing in with the chosen password: %d", status)
	}
}

// A child's login ID has no mailbox; there is nowhere to send an invite.
func TestAChildCannotBeInvited(t *testing.T) {
	srv, _ := newResetServer(t)
	admin := &client{t: t, srv: srv}
	admin.login("admin@jca.ac.th")
	status, acct, _ := admin.do("POST", "/api/v1/user-accounts", map[string]any{
		"email": "stu_invite_test", "password": "Temp-Pass-12", "role": "Student", "display_name": "Kid",
	})
	if status != 201 {
		t.Fatalf("creating a student login: %d (%v)", status, acct)
	}
	if status, _, _ := admin.do("POST", "/api/v1/user-accounts/"+acct["user_account_id"].(string)+"/invite", nil); status != 422 {
		t.Fatalf("inviting a child: %d, want 422", status)
	}
}

func TestOnlyTheOfficeSendsInvites(t *testing.T) {
	srv, _ := newResetServer(t)
	sandy := &client{t: t, srv: srv}
	sandy.login("sandy01234@gmail.com")
	if status, _, _ := sandy.do("POST", "/api/v1/user-accounts/usr_sandy/invite", nil); status != 403 {
		t.Fatalf("a parent sending invites: %d, want 403", status)
	}
}

// An address that already has an account is a conflict the console can act
// on, not a malformed request.
func TestATakenEmailIsAConflict(t *testing.T) {
	srv, _ := newResetServer(t)
	admin := &client{t: t, srv: srv}
	admin.login("admin@jca.ac.th")
	status, _, _ := admin.do("POST", "/api/v1/user-accounts", map[string]any{
		"email": "sandy01234@gmail.com", "password": "Another-Pass-12", "role": "Parent", "display_name": "Sandy Again",
	})
	if status != 409 {
		t.Fatalf("a second account on a taken address: %d, want 409", status)
	}
}

// The child's login goes out in the same email, checked first: a login that is
// not this parent's child's, or a password that is not the account's, is
// refused rather than mailed.
func TestTheInviteCarriesTheChildsLogin(t *testing.T) {
	srv, cap := newResetServer(t)
	admin := &client{t: t, srv: srv}
	admin.login("admin@jca.ac.th")

	send := func(logins []map[string]any) int {
		status, _, _ := admin.do("POST", "/api/v1/user-accounts/usr_sandy/invite", map[string]any{"studentLogins": logins})
		return status
	}

	if status := send([]map[string]any{{"name": "Penny", "loginId": "penny@jca.ac.th", "password": db.DevPassword}}); status != 202 {
		t.Fatalf("invite with Penny's login: %d", status)
	}
	_, body := cap.last()
	for _, want := range []string{"Penny's login ID: penny@jca.ac.th", "Penny's password: " + db.DevPassword, "Penny can sign in to the JTrax student app"} {
		if !strings.Contains(body, want) {
			t.Errorf("the invite does not say %q:\n%s", want, body)
		}
	}

	if status := send([]map[string]any{{"name": "Penny", "loginId": "penny@jca.ac.th", "password": "not-her-password-1"}}); status != 422 {
		t.Fatalf("a wrong password was mailed: %d", status)
	}

	status, acct, _ := admin.do("POST", "/api/v1/user-accounts", map[string]any{
		"email": "stu_someone_else", "password": "Their-Pass-12", "role": "Student", "display_name": "Someone",
	})
	if status != 201 {
		t.Fatalf("creating an unrelated student: %d (%v)", status, acct)
	}
	if status := send([]map[string]any{{"name": "Someone", "loginId": "stu_someone_else", "password": "Their-Pass-12"}}); status != 422 {
		t.Fatalf("another family's child's login was mailed: %d", status)
	}
}
