package api_test

/* An admin setting an account's password from the console ends that
   account's sessions — but not the admin's own when it is their account. */

import "testing"

func TestAdminPasswordResetEndsTheAccountsSessions(t *testing.T) {
	srv := newServer(t)
	admin := &client{t: t, srv: srv}
	admin.login("admin@jca.ac.th")
	teacher := &client{t: t, srv: srv}
	teacher.login("serene@jca.ac.th")

	if s, out, _ := admin.do("PATCH", "/api/v1/user-accounts/usr_serene", map[string]any{"password": "Fresh-Start-9"}); s != 200 {
		t.Fatalf("reset: %d (%v)", s, out)
	}
	if s, _, _ := teacher.do("GET", "/api/v1/auth/me", nil); s != 401 {
		t.Fatalf("the teacher's old session still works: %d", s)
	}
	if signIn(&client{t: t, srv: srv}, "serene@jca.ac.th", "Fresh-Start-9") != 200 {
		t.Fatal("the new password does not work")
	}
	if s, _, _ := admin.do("GET", "/api/v1/auth/me", nil); s != 200 {
		t.Fatalf("resetting someone else signed the admin out: %d", s)
	}
}

func TestAdminChangingOwnPasswordKeepsOnlyThisSession(t *testing.T) {
	srv := newServer(t)
	admin := &client{t: t, srv: srv}
	admin.login("admin@jca.ac.th")
	elsewhere := &client{t: t, srv: srv}
	elsewhere.login("admin@jca.ac.th")

	if s, out, _ := admin.do("PATCH", "/api/v1/user-accounts/usr_admin1", map[string]any{"password": "Fresh-Start-9"}); s != 200 {
		t.Fatalf("change: %d (%v)", s, out)
	}
	if s, _, _ := admin.do("GET", "/api/v1/auth/me", nil); s != 200 {
		t.Fatalf("the session making the change was signed out: %d", s)
	}
	if s, _, _ := elsewhere.do("GET", "/api/v1/auth/me", nil); s != 401 {
		t.Fatalf("the admin's other session still works: %d", s)
	}
}
