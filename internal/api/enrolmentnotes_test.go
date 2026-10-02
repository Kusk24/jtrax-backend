// The office's note on an enrolment, and the date it started, are editable
// from the enrolment modal — and editing one leaves the other alone.
package api_test

import "testing"

func TestAnEnrolmentKeepsItsNote(t *testing.T) {
	c := &client{t: t, srv: newServer(t)}
	c.login("admin@jca.ac.th")

	status, edited, _ := c.do("PATCH", "/api/v1/enrollments/enr_penny", map[string]any{
		"notes": "Moving to Saturdays after the exam",
	})
	if status != 200 || edited["notes"] != "Moving to Saturdays after the exam" {
		t.Fatalf("note: %d (%v)", status, edited)
	}

	status, redated, _ := c.do("PATCH", "/api/v1/enrollments/enr_penny", map[string]any{
		"enrolled_date": "2026-01-12",
	})
	if status != 200 || redated["enrolled_date"] != "2026-01-12" || redated["notes"] != "Moving to Saturdays after the exam" {
		t.Fatalf("changing the date lost the note: %d (%v)", status, redated)
	}
}

// Ending an enrolment records the day, so the course history can say when.
func TestAnEnrolmentKeepsTheDayItEnded(t *testing.T) {
	c := &client{t: t, srv: newServer(t)}
	c.login("admin@jca.ac.th")

	status, ended, _ := c.do("PATCH", "/api/v1/enrollments/enr_penny", map[string]any{
		"status": "Withdrawn", "ended_date": "2026-09-27",
	})
	if status != 200 || ended["ended_date"] != "2026-09-27" || ended["status"] != "Withdrawn" {
		t.Fatalf("ending: %d (%v)", status, ended)
	}
}

// A deleted enrolment is kept for the office's history and hidden from the
// family.
func TestADeletedEnrolmentIsKeptButHiddenFromTheFamily(t *testing.T) {
	srv := newServer(t)
	admin := &client{t: t, srv: srv}
	admin.login("admin@jca.ac.th")
	status, _, _ := admin.do("PATCH", "/api/v1/enrollments/enr_penny", map[string]any{
		"status": "Withdrawn", "ended_date": "2026-09-27", "deleted_date": "2026-09-27",
	})
	if status != 200 {
		t.Fatalf("delete: %d", status)
	}
	if status, got, _ := admin.do("GET", "/api/v1/enrollments/enr_penny", nil); status != 200 || got["deleted_date"] != "2026-09-27" {
		t.Fatalf("the office should still see it: %d (%v)", status, got)
	}

	penny := &client{t: t, srv: srv}
	penny.login("penny@jca.ac.th")
	if status, _, _ := penny.do("GET", "/api/v1/enrollments/enr_penny", nil); status != 404 {
		t.Fatalf("the student should not see a deleted enrolment: %d", status)
	}
}
