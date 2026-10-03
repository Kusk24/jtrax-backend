package api_test

import (
	"strings"
	"testing"
)

// newSession puts a Beginner session (Penny's class) on the given day.
func newSession(t *testing.T, admin *client, day string) string {
	t.Helper()
	status, obj, _ := admin.do("POST", "/api/v1/class-sessions", map[string]any{
		"class_id": "cls_beg101", "session_date": day, "start_time": "16:00", "end_time": "17:30",
	})
	if status != 201 {
		t.Fatalf("create session: %d (%v)", status, obj)
	}
	return obj["session_id"].(string)
}

func TestCancellingAnUpcomingClassTellsTheFamilyAndRefunds(t *testing.T) {
	d := newDB(t)
	srv := newServerOn(t, d)
	admin := &client{t: t, srv: srv}
	admin.login("admin@jca.ac.th")
	sesID := newSession(t, admin, academyDay(2))

	// The seeded purchase expired on 2026-05-20, and an expired balance is
	// refused at check-in — give Penny credits still good on the class day.
	if _, err := d.Exec(`INSERT INTO credit_transaction
		(credit_transaction_id, enrollment_id, transaction_type, amount, expiry_date, transaction_date)
		VALUES ('ctx_topup', 'enr_penny', 'purchase', 10, ?, ?)`, academyDay(30), academyDay(0)); err != nil {
		t.Fatalf("top up: %v", err)
	}

	// Penny is already on the roster and was charged for it.
	teacher := &client{t: t, srv: srv}
	teacher.login("serene@jca.ac.th")
	status, att, _ := teacher.do("POST", "/api/v1/attendance", map[string]any{
		"student_id": "stu_penny", "session_id": sesID, "check_in_time": academyDay(2) + "T15:59:00",
	})
	if status != 201 {
		t.Fatalf("check-in: %d", status)
	}
	attID := att["attendance_id"].(string)
	teacher.do("PATCH", "/api/v1/attendance/"+attID, map[string]any{"check_out_time": academyDay(2) + "T17:31:00"})
	var charged int
	d.QueryRow(`SELECT COUNT(*) FROM credit_transaction WHERE attendance_id = ?`, attID).Scan(&charged)
	if charged != 1 {
		t.Fatalf("setup: the attendance should have been charged once, got %d", charged)
	}

	status, out, _ := admin.do("POST", "/api/v1/class-sessions/"+sesID+"/cancel", nil)
	if status != 200 || out["cancelled"] != true {
		t.Fatalf("cancel: %d %v", status, out)
	}

	// The credit comes back and the roster is emptied, but the session stays,
	// marked cancelled, so the desk still sees it on the day.
	var left, attendance int
	var cancelledAt *string
	d.QueryRow(`SELECT COUNT(*) FROM credit_transaction WHERE attendance_id = ?`, attID).Scan(&left)
	d.QueryRow(`SELECT COUNT(*) FROM attendance WHERE session_id = ?`, sesID).Scan(&attendance)
	d.QueryRow(`SELECT cancelled_at FROM class_session WHERE session_id = ?`, sesID).Scan(&cancelledAt)
	if left != 0 || attendance != 0 || cancelledAt == nil {
		t.Fatalf("after cancel: %d charges, %d attendance, cancelled_at %v", left, attendance, cancelledAt)
	}

	// Sandy is Penny's mother: told once, with the class and the day.
	in := inbox(t, srv, "sandy01234@gmail.com")
	if got := countType(in, "class_cancelled"); got != 1 {
		t.Fatalf("parent should be told once, got %d", got)
	}
	body := bodyOfType(in, "class_cancelled")
	if !strings.Contains(body, "16:00") || !strings.Contains(body, "Penny") {
		t.Fatalf("the notice should say which class and which child: %q", body)
	}
}

// Cancelling a session that already happened is tidying, not news.
func TestCancellingAPastClassTellsNobody(t *testing.T) {
	srv := newServer(t)
	admin := &client{t: t, srv: srv}
	admin.login("admin@jca.ac.th")
	sesID := newSession(t, admin, academyDay(-7))

	status, out, _ := admin.do("POST", "/api/v1/class-sessions/"+sesID+"/cancel", nil)
	if status != 200 || out["students_notified"] != float64(0) {
		t.Fatalf("cancel past: %d %v", status, out)
	}
	if got := countType(inbox(t, srv, "sandy01234@gmail.com"), "class_cancelled"); got != 0 {
		t.Fatalf("a past class should not be announced, got %d", got)
	}
}

func TestOnlyTheOfficeCancelsAClass(t *testing.T) {
	srv := newServer(t)
	admin := &client{t: t, srv: srv}
	admin.login("admin@jca.ac.th")
	sesID := newSession(t, admin, academyDay(2))

	for _, who := range []string{"serene@jca.ac.th", "sandy01234@gmail.com"} {
		c := &client{t: t, srv: srv}
		c.login(who)
		if status, _, _ := c.do("POST", "/api/v1/class-sessions/"+sesID+"/cancel", nil); status != 403 {
			t.Errorf("%s cancelling a class: want 403, got %d", who, status)
		}
	}
	if status, _, _ := admin.do("POST", "/api/v1/class-sessions/ses_nope/cancel", nil); status != 404 {
		t.Errorf("unknown session: want 404, got %d", status)
	}
}

// A cancelled class stays on the office's timetable, marked, and nowhere else.
func TestACancelledClassIsKeptForTheOfficeOnly(t *testing.T) {
	srv := newServer(t)
	admin := &client{t: t, srv: srv}
	admin.login("admin@jca.ac.th")
	sesID := newSession(t, admin, academyDay(2))
	if status, out, _ := admin.do("POST", "/api/v1/class-sessions/"+sesID+"/cancel", nil); status != 200 {
		t.Fatalf("cancel: %d %v", status, out)
	}

	status, row, _ := admin.do("GET", "/api/v1/class-sessions/"+sesID, nil)
	if status != 200 || row["cancelled_at"] == nil {
		t.Fatalf("the office should still see the session, marked cancelled: %d %v", status, row)
	}

	for _, who := range []string{"serene@jca.ac.th", "sandy01234@gmail.com", "penny@jca.ac.th"} {
		c := &client{t: t, srv: srv}
		c.login(who)
		_, _, rows := c.do("GET", "/api/v1/class-sessions", nil)
		for _, r := range rows {
			if r["session_id"] == sesID {
				t.Errorf("%s should not see a cancelled class", who)
			}
		}
	}

	// Cancelling twice is a class that is already gone.
	if status, _, _ := admin.do("POST", "/api/v1/class-sessions/"+sesID+"/cancel", nil); status != 404 {
		t.Errorf("second cancel: want 404, got %d", status)
	}
	// And nobody can be checked into it.
	if status, _, _ := admin.do("POST", "/api/v1/attendance", map[string]any{
		"student_id": "stu_penny", "session_id": sesID,
	}); status != 409 {
		t.Errorf("check-in to a cancelled class: want 409, got %d", status)
	}
}
