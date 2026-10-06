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
	for _, want := range []string{
		"Hello Sandy Jones", "We're happy to have you and Penny and Uri with us.",
		"Parent Account", "Your sign-in email: sandy01234@gmail.com",
		"Set Your Password:", "/reset-password?token=", "Open JTrax:",
		"valid for 7 days", "Best regards,", "JTrax Parent Portal",
	} {
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

// An older student with their own address gets a link too — worded for a
// student, not the parent's welcome about "your child's" classes.
func TestAStudentWithAnEmailGetsAStudentPasswordLink(t *testing.T) {
	srv, cap := newResetServer(t)
	admin := &client{t: t, srv: srv}
	admin.login("admin@jca.ac.th")
	status, acct, _ := admin.do("POST", "/api/v1/user-accounts", map[string]any{
		"email": "mint.student@example.com", "password": "Temp-Pass-12", "role": "Student", "display_name": "Mint",
	})
	if status != 201 {
		t.Fatalf("creating a student account: %d (%v)", status, acct)
	}
	if status, got, _ := admin.do("POST", "/api/v1/user-accounts/"+acct["user_account_id"].(string)+"/invite", nil); status != 202 {
		t.Fatalf("invite: %d (%v)", status, got)
	}
	to, body := cap.last()
	if to != "mint.student@example.com" {
		t.Fatalf("sent to %q", to)
	}
	if !strings.Contains(body, "Your JTrax student account has been created") || strings.Contains(body, "parent account") {
		t.Fatalf("the student's email should be worded for a student:\n%s", body)
	}
	anon := &client{t: t, srv: srv}
	if status, _, _ := anon.do("POST", "/api/v1/auth/reset-password", map[string]any{
		"token": tokenFrom(body), "password": "Chosen-by-Mint-1",
	}); status != 200 {
		t.Fatalf("setting the password from the link: %d", status)
	}

	// And they really can sign in with that address — typed however they
	// type it — and arrive as a student.
	for _, typed := range []string{"mint.student@example.com", "  Mint.Student@Example.COM "} {
		fresh := &client{t: t, srv: srv}
		status, out, _ := fresh.do("POST", "/api/v1/auth/login", map[string]string{
			"email": typed, "password": "Chosen-by-Mint-1",
		})
		if status != 200 {
			t.Fatalf("student signing in with %q: %d (%v)", typed, status, out)
		}
		if user, _ := out["user"].(map[string]any); user["role"] != "Student" {
			t.Fatalf("signed in as %v, want Student", out["user"])
		}
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
	for _, want := range []string{"Student Login", "Login ID: penny@jca.ac.th", "Password: " + db.DevPassword, "Penny can also sign in to the JTrax student app"} {
		if !strings.Contains(body, want) {
			t.Errorf("the invite does not say %q:\n%s", want, body)
		}
	}
	/* Penny signs in with her own email, so resets it herself: no guide. */
	if strings.Contains(body, "Account & Security") {
		t.Errorf("a child with their own email was given the parent's reset guide:\n%s", body)
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

// A child who signs in with a username has their password reset by the
// parent, so the welcome says where: Home, their card, Account & Security.
func TestTheInviteShowsWhereToResetAChildsPassword(t *testing.T) {
	srv, cap := newResetServer(t)
	admin := &client{t: t, srv: srv}
	admin.login("admin@jca.ac.th")
	_, acct, _ := admin.do("POST", "/api/v1/user-accounts", map[string]any{
		"email": "stu_mia", "password": "Temp-Pass-12", "role": "Student", "display_name": "Mia",
	})
	_, stu, _ := admin.do("POST", "/api/v1/students", map[string]any{"name": "Mia", "user_account_id": acct["user_account_id"]})
	admin.do("POST", "/api/v1/student-parents", map[string]any{"student_id": stu["student_id"], "parent_id": "par_sandy"})

	status, out, _ := admin.do("POST", "/api/v1/user-accounts/usr_sandy/invite", map[string]any{
		"studentLogins": []map[string]any{{"name": "Mia", "loginId": "stu_mia", "password": "Temp-Pass-12"}},
	})
	if status != 202 {
		t.Fatalf("invite: %d %v", status, out)
	}
	_, body := cap.last()
	for _, want := range []string{
		"If Mia forgets the password", "On Home, tap Mia's card", "Scroll down to Account & Security", "Tap Reset Password",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the invite does not say %q:\n%s", want, body)
		}
	}
}
