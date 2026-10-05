package api_test

/* A parent setting a new password for their own child, from the portal. */

import (
	"strings"
	"testing"
	"time"
)

// childWithLogin registers a child of Sandy who signs in with `login`, and
// returns the student id.
func childWithLogin(t *testing.T, admin *client, login string) string {
	t.Helper()
	status, acct, _ := admin.do("POST", "/api/v1/user-accounts", map[string]any{
		"email": login, "password": "tiger-lamp-42", "role": "Student", "display_name": "Noe",
	})
	if status != 201 {
		t.Fatalf("child account: %d (%v)", status, acct)
	}
	_, st, _ := admin.do("POST", "/api/v1/students", map[string]any{"user_account_id": acct["user_account_id"], "name": "Noe"})
	admin.do("POST", "/api/v1/student-parents", map[string]any{
		"student_id": st["student_id"], "parent_id": "par_sandy", "relationship_type": "Mother",
	})
	return st["student_id"].(string)
}

func signIn(c *client, login, password string) int {
	status, out, _ := c.do("POST", "/api/v1/auth/login", map[string]any{"email": login, "password": password})
	if tok, ok := out["token"].(string); ok {
		c.token = tok
	}
	return status
}

func TestAParentSetsTheirChildsPassword(t *testing.T) {
	srv, cap := newResetServer(t)
	admin := &client{t: t, srv: srv}
	admin.login("admin@jca.ac.th")
	noe := childWithLogin(t, admin, "stu_noe")

	child := &client{t: t, srv: srv}
	if signIn(child, "stu_noe", "tiger-lamp-42") != 200 {
		t.Fatal("the child could not sign in to start with")
	}

	parent := &client{t: t, srv: srv}
	parent.login("sandy01234@gmail.com")
	if _, out, _ := parent.do("GET", "/api/v1/students/"+noe+"/login", nil); out["login"] != "stu_noe" || out["ownEmail"] != false {
		t.Fatalf("login: %v", out)
	}
	path := "/api/v1/students/" + noe + "/password"
	if s, _, _ := parent.do("POST", path, map[string]any{"password": "short"}); s != 400 {
		t.Fatalf("a weak password: want 400, got %d", s)
	}
	if s, out, _ := parent.do("POST", path, map[string]any{"password": "Brand-New-Pass-7"}); s != 204 {
		t.Fatalf("reset: %d (%v)", s, out)
	}

	// Signed out everywhere, and in with the new password only.
	if s, _, _ := child.do("GET", "/api/v1/auth/me", nil); s != 401 {
		t.Fatalf("the child's old session still works: %d", s)
	}
	if signIn(&client{t: t, srv: srv}, "stu_noe", "tiger-lamp-42") == 200 {
		t.Fatal("the old password still works")
	}
	if signIn(&client{t: t, srv: srv}, "stu_noe", "Brand-New-Pass-7") != 200 {
		t.Fatal("the new password does not work")
	}

	// The parent is told it was done.
	deadline := time.Now().Add(2 * time.Second)
	for {
		to, body := cap.last()
		if to == "sandy01234@gmail.com" && strings.Contains(body, "Noe's JTrax student account was changed") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no notice to the parent; last %q:\n%s", to, body)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestOnlyAParentOfAUsernameChildCanSetIt(t *testing.T) {
	srv, _ := newResetServer(t)
	admin := &client{t: t, srv: srv}
	admin.login("admin@jca.ac.th")
	withEmail := childWithLogin(t, admin, "noe.own@example.com")

	parent := &client{t: t, srv: srv}
	parent.login("sandy01234@gmail.com")
	if s, _, _ := parent.do("POST", "/api/v1/students/"+withEmail+"/password", map[string]any{"password": "Brand-New-Pass-7"}); s != 409 {
		t.Fatalf("a child with their own email: want 409, got %d", s)
	}
	if s, _, _ := parent.do("POST", "/api/v1/students/stu_nobody/password", map[string]any{"password": "Brand-New-Pass-7"}); s != 404 {
		t.Fatalf("somebody else's child: want 404, got %d", s)
	}
	if s, _, _ := admin.do("POST", "/api/v1/students/"+withEmail+"/password", map[string]any{"password": "Brand-New-Pass-7"}); s != 403 {
		t.Fatalf("staff: want 403, got %d", s)
	}
}
