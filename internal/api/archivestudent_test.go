// A student who leaves is archived: they cannot sign in or be checked in, and
// restoring them undoes both. Their records stay.
package api_test

import (
	"testing"

	"github.com/Kusk24/jtrax-backend/internal/db"
)

func TestArchivedStudentIsOutOfDayToDay(t *testing.T) {
	srv := newServer(t)
	admin := &client{t: t, srv: srv}
	admin.login("admin@jca.ac.th")

	// Penny is signed in before she is archived.
	penny := &client{t: t, srv: srv}
	penny.login("penny@jca.ac.th")

	if status, body, _ := admin.do("PATCH", "/api/v1/students/stu_penny", map[string]any{
		"archived_at": "2026-10-12", "archived_reason": "Moved away",
	}); status != 200 {
		t.Fatalf("archive: %d %v", status, body)
	}

	// Her session ends, and she cannot sign in again.
	if status, _, _ := penny.do("GET", "/api/v1/auth/me", nil); status != 401 {
		t.Fatalf("archived session: want 401, got %d", status)
	}
	status, body, _ := penny.do("POST", "/api/v1/auth/login", map[string]string{"email": "penny@jca.ac.th", "password": db.DevPassword})
	if status != 403 {
		t.Fatalf("archived login: want 403, got %d %v", status, body)
	}

	// She cannot be checked in.
	session := sessionOf(t, admin, "King Slayer", "2026-10-12", "10:00", "12:00")
	if status, body, _ := admin.do("POST", "/api/v1/attendance", map[string]any{
		"student_id": "stu_penny", "session_id": session,
	}); status != 409 {
		t.Fatalf("archived check-in: want 409, got %d %v", status, body)
	}

	// Her history is still there.
	if _, _, list := admin.do("GET", "/api/v1/attendance?student_id=stu_penny", nil); len(list) == 0 {
		t.Fatalf("archived student's attendance should be kept")
	}

	// Restored: signs in and checks in again.
	if status, body, _ := admin.do("PATCH", "/api/v1/students/stu_penny", map[string]any{
		"archived_at": nil, "archived_reason": nil,
	}); status != 200 {
		t.Fatalf("restore: %d %v", status, body)
	}
	penny.login("penny@jca.ac.th")
	if status, body, _ := admin.do("POST", "/api/v1/attendance", map[string]any{
		"student_id": "stu_penny", "session_id": session,
	}); status != 201 {
		t.Fatalf("restored check-in: want 201, got %d %v", status, body)
	}
}

// A guardian archived with their last child: no sign-in, no announcements to
// every parent; restored, both come back. Their record stays.
func TestArchivedParentIsOutOfDayToDay(t *testing.T) {
	srv := newServer(t)
	admin := &client{t: t, srv: srv}
	admin.login("admin@jca.ac.th")
	sandy := &client{t: t, srv: srv}
	sandy.login("sandy01234@gmail.com")

	if status, body, _ := admin.do("PATCH", "/api/v1/parents/par_sandy", map[string]any{"archived_at": "2026-10-14"}); status != 200 {
		t.Fatalf("archive parent: %d %v", status, body)
	}
	if status, _, _ := sandy.do("GET", "/api/v1/auth/me", nil); status != 401 {
		t.Fatalf("archived parent's session: want 401, got %d", status)
	}
	if status, _, _ := sandy.do("POST", "/api/v1/auth/login", map[string]string{"email": "sandy01234@gmail.com", "password": db.DevPassword}); status != 403 {
		t.Fatalf("archived parent's login: want 403, got %d", status)
	}

	// An announcement to everyone does not reach her.
	if status, body, _ := admin.do("POST", "/api/v1/announcements", map[string]any{
		"title": "Holiday", "body": "No class on Monday.", "author_user_account_id": "usr_admin1",
	}); status != 201 {
		t.Fatalf("announcement: %d %v", status, body)
	}
	if status, _, _ := admin.do("GET", "/api/v1/parents/par_sandy", nil); status != 200 {
		t.Fatalf("an archived parent's record is kept, got %d", status)
	}

	// Restored: signs in, and the next announcement reaches her.
	if status, body, _ := admin.do("PATCH", "/api/v1/parents/par_sandy", map[string]any{"archived_at": nil}); status != 200 {
		t.Fatalf("restore parent: %d %v", status, body)
	}
	if got := countType(inbox(t, srv, "sandy01234@gmail.com"), "announcement"); got != 0 {
		t.Fatalf("the announcement sent while archived reached her: %d", got)
	}
	admin.do("POST", "/api/v1/announcements", map[string]any{
		"title": "Welcome back", "body": "Classes resume.", "author_user_account_id": "usr_admin1",
	})
	if got := countType(inbox(t, srv, "sandy01234@gmail.com"), "announcement"); got != 1 {
		t.Fatalf("restored, she should get the next announcement: %d", got)
	}
}
