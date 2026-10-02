// A child in King Slayer from 10:00 to 12:00 cannot also be put into Summer
// Challenger from 11:00 to 12:00 — whoever writes the attendance.
package api_test

import (
	"strings"
	"testing"
)

func sessionOf(t *testing.T, c *client, className, date, start, end string) string {
	t.Helper()
	_, class, _ := c.do("POST", "/api/v1/classes", map[string]any{"name": className, "class_type": "Group"})
	_, session, _ := c.do("POST", "/api/v1/class-sessions", map[string]any{
		"class_id": class["class_id"], "session_date": date,
		"start_time": start, "end_time": end, "session_status": "Ongoing",
	})
	return session["session_id"].(string)
}

func TestAStudentCannotJoinTwoOverlappingClasses(t *testing.T) {
	c := &client{t: t, srv: newServer(t)}
	c.login("admin@jca.ac.th")
	kingSlayer := sessionOf(t, c, "King Slayer", "2026-08-21", "10:00", "12:00")
	summer := sessionOf(t, c, "Summer Challenger", "2026-08-21", "11:00", "12:00")
	_, student, _ := c.do("POST", "/api/v1/students", map[string]any{"name": "Mini"})

	if status, body, _ := c.do("POST", "/api/v1/attendance", map[string]any{
		"student_id": student["student_id"], "session_id": kingSlayer,
	}); status != 201 {
		t.Fatalf("first class: %d (%v)", status, body)
	}
	status, body, _ := c.do("POST", "/api/v1/attendance", map[string]any{
		"student_id": student["student_id"], "session_id": summer,
	})
	if status != 409 {
		t.Fatalf("overlapping class should be refused with 409, got %d (%v)", status, body)
	}
	if msg, _ := body["error"].(string); !strings.Contains(msg, "King Slayer") {
		t.Fatalf("refusal should name the class they are in, got %v", body)
	}
}

func TestBackToBackAndOtherDaysDoNotClash(t *testing.T) {
	c := &client{t: t, srv: newServer(t)}
	c.login("admin@jca.ac.th")
	morning := sessionOf(t, c, "King Slayer", "2026-08-21", "10:00", "12:00")
	noon := sessionOf(t, c, "Summer Challenger", "2026-08-21", "12:00", "13:00")
	nextDay := sessionOf(t, c, "Master", "2026-08-22", "10:30", "11:30")
	_, student, _ := c.do("POST", "/api/v1/students", map[string]any{"name": "Mini"})

	for _, s := range []string{morning, noon, nextDay} {
		if status, body, _ := c.do("POST", "/api/v1/attendance", map[string]any{
			"student_id": student["student_id"], "session_id": s,
		}); status != 201 {
			t.Fatalf("session %s: %d (%v)", s, status, body)
		}
	}
}

// Once a student has checked out, that class is over for them.
func TestACheckedOutStudentCanJoinAnotherClass(t *testing.T) {
	c := &client{t: t, srv: newServer(t)}
	c.login("admin@jca.ac.th")
	kingSlayer := sessionOf(t, c, "King Slayer", "2026-08-21", "10:00", "12:00")
	summer := sessionOf(t, c, "Summer Challenger", "2026-08-21", "11:00", "12:00")
	_, student, _ := c.do("POST", "/api/v1/students", map[string]any{"name": "Mini"})

	_, att, _ := c.do("POST", "/api/v1/attendance", map[string]any{
		"student_id": student["student_id"], "session_id": kingSlayer,
	})
	c.do("PATCH", "/api/v1/attendance/"+att["attendance_id"].(string), map[string]any{"check_out_time": "2026-08-21T03:30:00Z"})

	if status, body, _ := c.do("POST", "/api/v1/attendance", map[string]any{
		"student_id": student["student_id"], "session_id": summer,
	}); status != 201 {
		t.Fatalf("checked out of King Slayer, should join Summer Challenger: %d (%v)", status, body)
	}
}
