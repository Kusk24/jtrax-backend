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
