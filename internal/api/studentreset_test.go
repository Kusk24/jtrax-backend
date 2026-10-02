// A child who forgot their password, and anyone changing their own.
package api_test

import (
	"strings"
	"testing"

	"github.com/Kusk24/jtrax-backend/internal/db"
)

// childOfSandy registers a child who signs in with an ID, as the console does.
func childOfSandy(t *testing.T, admin *client) {
	t.Helper()
	status, acct, _ := admin.do("POST", "/api/v1/user-accounts", map[string]any{
		"email": "stu_mini_kid", "password": "tiger-lamp-42", "role": "Student", "display_name": "Mini",
	})
	if status != 201 {
		t.Fatalf("child account: %d (%v)", status, acct)
	}
	status, st, _ := admin.do("POST", "/api/v1/students", map[string]any{
		"user_account_id": acct["user_account_id"], "name": "Mini",
	})
	if status != 201 {
		t.Fatalf("child: %d (%v)", status, st)
	}
	if status, _, _ := admin.do("POST", "/api/v1/student-parents", map[string]any{
		"student_id": st["student_id"], "parent_id": "par_sandy", "relationship_type": "Mother",
	}); status != 201 {
		t.Fatalf("family link: %d", status)
	}
}

func TestAChildsResetLinkGoesToTheirParent(t *testing.T) {
	srv, cap := newResetServer(t)
	admin := &client{t: t, srv: srv}
	admin.login("admin@jca.ac.th")
	childOfSandy(t, admin)

	anon := &client{t: t, srv: srv}
	if status, _, _ := anon.do("POST", "/api/v1/auth/forgot-password", map[string]any{"email": "stu_mini_kid"}); status != 202 {
		t.Fatalf("forgot password for a child: %d", status)
	}
	to, body := cap.last()
	if to != "sandy01234@gmail.com" {
		t.Fatalf("the child's link went to %q, want their mother", to)
	}
	for _, want := range []string{"Hello Sandy Jones", "Mini asked to reset", "Mini's login ID: stu_mini_kid", "/reset-password?token="} {
		if !strings.Contains(body, want) {
			t.Errorf("the email does not say %q:\n%s", want, body)
		}
	}

	// The link resets the child's password, not the parent's.
	if status, _, _ := anon.do("POST", "/api/v1/auth/reset-password", map[string]any{
		"token": tokenFrom(body), "password": "river-moon-77",
	}); status != 200 {
		t.Fatalf("resetting from the link: %d", status)
	}
	if status, _, _ := anon.do("POST", "/api/v1/auth/login", map[string]string{"email": "stu_mini_kid", "password": "river-moon-77"}); status != 200 {
		t.Fatalf("the child signing in with the new password: %d", status)
	}
	if status, _, _ := anon.do("POST", "/api/v1/auth/login", map[string]string{"email": "sandy01234@gmail.com", "password": db.DevPassword}); status != 200 {
		t.Fatalf("the parent's own password changed too: %d", status)
	}
}

func TestAnUnknownChildIDSendsNothingAndSaysTheSame(t *testing.T) {
	srv, cap := newResetServer(t)
	anon := &client{t: t, srv: srv}
	status, obj, _ := anon.do("POST", "/api/v1/auth/forgot-password", map[string]any{"email": "stu_nobody"})
	if status != 202 || !strings.Contains(obj["status"].(string), "if that email has an account") {
		t.Fatalf("an unknown ID answered differently: %d (%v)", status, obj)
	}
	if to, _ := cap.last(); to != "" {
		t.Fatalf("something was sent to %q", to)
	}
}

func TestChangingYourOwnPassword(t *testing.T) {
	srv := newServer(t)
	sandy := &client{t: t, srv: srv}
	sandy.login("sandy01234@gmail.com")
	other := &client{t: t, srv: srv}
	other.login("sandy01234@gmail.com") // her phone

	if status, _, _ := sandy.do("POST", "/api/v1/auth/change-password", map[string]any{
		"currentPassword": "not-it-123", "newPassword": "Sandys-New-1",
	}); status != 400 {
		t.Fatalf("a wrong current password was accepted: %d", status)
	}
	if status, _, _ := sandy.do("POST", "/api/v1/auth/change-password", map[string]any{
		"currentPassword": db.DevPassword, "newPassword": "short",
	}); status != 400 {
		t.Fatalf("a too-weak new password was accepted: %d", status)
	}
	if status, _, _ := sandy.do("POST", "/api/v1/auth/change-password", map[string]any{
		"currentPassword": db.DevPassword, "newPassword": "Sandys-New-1",
	}); status != 200 {
		t.Fatalf("changing the password: %d", status)
	}

	// This session stays; the other device is signed out.
	if status, _, _ := sandy.do("GET", "/api/v1/auth/me", nil); status != 200 {
		t.Fatalf("the session that changed it was signed out: %d", status)
	}
	if status, _, _ := other.do("GET", "/api/v1/auth/me", nil); status != 401 {
		t.Fatalf("another device stayed signed in: %d", status)
	}
	anon := &client{t: t, srv: srv}
	if status, _, _ := anon.do("POST", "/api/v1/auth/login", map[string]string{"email": "sandy01234@gmail.com", "password": "Sandys-New-1"}); status != 200 {
		t.Fatalf("signing in with the new password: %d", status)
	}
}
