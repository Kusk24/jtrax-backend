// One credit is one hour, and half an hour is half a credit. These tests care
// mostly about the fractions surviving: a balance that silently rounds 1.5 to
// 1 or 2 is worse than one that never moved at all.
package api_test

import (
	"math"
	"strings"
	"testing"
)

// balanceOf sums the ledger for one enrolment, the way every screen does.
func balanceOf(c *client, enrolmentID string) float64 {
	_, _, rows := c.do("GET", "/api/v1/credit-transactions", nil)
	total := 0.0
	for _, r := range rows {
		if r["enrollment_id"] == enrolmentID {
			amount, _ := r["amount"].(float64)
			total += amount
		}
	}
	return total
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// desk sets up a class, a student enrolled in it, and an opening balance.
func desk(t *testing.T, c *client, start, end string, opening float64) (studentID, sessionID, enrolmentID string) {
	t.Helper()
	_, class, _ := c.do("POST", "/api/v1/classes", map[string]any{"name": "Half Hour Club", "class_type": "Group"})
	_, session, _ := c.do("POST", "/api/v1/class-sessions", map[string]any{
		"class_id": class["class_id"], "session_date": "2026-08-21",
		"start_time": start, "end_time": end, "session_status": "Ongoing",
	})
	_, student, _ := c.do("POST", "/api/v1/students", map[string]any{"name": "Half Hour Child"})
	_, enrolment, _ := c.do("POST", "/api/v1/enrollments", map[string]any{
		"student_id": student["student_id"], "class_id": class["class_id"],
		"enrolled_date": "2026-08-01", "status": "Active",
	})
	if opening != 0 {
		c.do("POST", "/api/v1/credit-transactions", map[string]any{
			"enrollment_id": enrolment["enrollment_id"], "transaction_type": "purchase",
			"amount": opening, "transaction_date": "2026-08-01",
		})
	}
	return student["student_id"].(string), session["session_id"].(string), enrolment["enrollment_id"].(string)
}

func TestHalfHourCostsHalfACredit(t *testing.T) {
	c := &client{t: t, srv: newServer(t)}
	c.login("admin@jca.ac.th")
	studentID, sessionID, enrolmentID := desk(t, c, "14:00", "14:30", 14)

	status, att, _ := c.do("POST", "/api/v1/attendance", map[string]any{
		"student_id": studentID, "session_id": sessionID, "check_in_time": "2026-08-21T07:00:00Z",
	})
	if status != 201 {
		t.Fatalf("check in: %d (%v)", status, att)
	}

	if got := balanceOf(c, enrolmentID); !near(got, 13.5) {
		t.Fatalf("14 credits minus half an hour should be 13.5, got %v", got)
	}
}

func TestNinetyMinutesCostsOneAndAHalf(t *testing.T) {
	c := &client{t: t, srv: newServer(t)}
	c.login("admin@jca.ac.th")
	studentID, sessionID, enrolmentID := desk(t, c, "09:00", "10:30", 10)

	c.do("POST", "/api/v1/attendance", map[string]any{
		"student_id": studentID, "session_id": sessionID, "check_in_time": "2026-08-21T02:00:00Z",
	})
	if got := balanceOf(c, enrolmentID); !near(got, 8.5) {
		t.Fatalf("want 8.5, got %v", got)
	}
}

// The console's session form never set duration_hours; the length has to come
// off the clock, or every session staff create is free.
func TestSessionLengthIsStoredFromTheClock(t *testing.T) {
	c := &client{t: t, srv: newServer(t)}
	c.login("admin@jca.ac.th")
	_, class, _ := c.do("POST", "/api/v1/classes", map[string]any{"name": "Clocked", "class_type": "Group"})
	_, session, _ := c.do("POST", "/api/v1/class-sessions", map[string]any{
		"class_id": class["class_id"], "session_date": "2026-08-21",
		"start_time": "10:00", "end_time": "11:30", "session_status": "Scheduled",
	})
	_, got, _ := c.do("GET", "/api/v1/class-sessions/"+session["session_id"].(string), nil)
	if hours, _ := got["duration_hours"].(float64); !near(hours, 1.5) {
		t.Fatalf("duration_hours should be worked out from the times, got %v", got["duration_hours"])
	}
}

// Undoing a check-in has to give the hour back, or a mistaken press costs a
// family real money.
func TestRemovingAnAttendanceRefundsIt(t *testing.T) {
	c := &client{t: t, srv: newServer(t)}
	c.login("admin@jca.ac.th")
	studentID, sessionID, enrolmentID := desk(t, c, "14:00", "14:30", 14)

	_, att, _ := c.do("POST", "/api/v1/attendance", map[string]any{
		"student_id": studentID, "session_id": sessionID, "check_in_time": "2026-08-21T07:00:00Z",
	})
	if got := balanceOf(c, enrolmentID); !near(got, 13.5) {
		t.Fatalf("want 13.5 after check in, got %v", got)
	}

	status, _, _ := c.do("DELETE", "/api/v1/attendance/"+att["attendance_id"].(string), nil)
	if status != 200 {
		t.Fatalf("delete: %d", status)
	}
	if got := balanceOf(c, enrolmentID); !near(got, 14) {
		t.Fatalf("the hour should have come back, got %v", got)
	}
}

// Moving a child between two classes they are enrolled in must change what the
// afternoon cost, not charge them for both.
func TestMovingToAnotherSessionRecharges(t *testing.T) {
	c := &client{t: t, srv: newServer(t)}
	c.login("admin@jca.ac.th")
	studentID, shortSession, enrolmentID := desk(t, c, "14:00", "14:30", 14)

	_, longClass, _ := c.do("POST", "/api/v1/classes", map[string]any{"name": "Long"})
	_, longSession, _ := c.do("POST", "/api/v1/class-sessions", map[string]any{
		"class_id": longClass["class_id"], "session_date": "2026-08-21",
		"start_time": "15:00", "end_time": "17:00", "session_status": "Ongoing",
	})
	// Enrolled in this one too — the desk only offers a child their own
	// classes, so a move between them is a move between enrolments.
	_, longEnrolment, _ := c.do("POST", "/api/v1/enrollments", map[string]any{
		"student_id": studentID, "class_id": longClass["class_id"],
		"enrolled_date": "2026-08-01", "status": "Active",
	})
	// Credits on this one too: an hour cannot be taken from a balance that
	// does not have it, so a move has to be affordable in the class moved to.
	c.do("POST", "/api/v1/credit-transactions", map[string]any{
		"enrollment_id": longEnrolment["enrollment_id"], "transaction_type": "purchase",
		"amount": 6, "transaction_date": "2026-08-01",
	})

	_, att, _ := c.do("POST", "/api/v1/attendance", map[string]any{
		"student_id": studentID, "session_id": shortSession, "check_in_time": "2026-08-21T07:00:00Z",
	})
	c.do("PATCH", "/api/v1/attendance/"+att["attendance_id"].(string), map[string]any{
		"session_id": longSession["session_id"],
	})

	// The half hour came back off the first enrolment...
	if got := balanceOf(c, enrolmentID); !near(got, 14) {
		t.Fatalf("the short class should have been refunded, got %v", got)
	}
	// ...and the two hours went on the class they actually attended.
	if got := balanceOf(c, longEnrolment["enrollment_id"].(string)); !near(got, 4) {
		t.Fatalf("want 4 on the two-hour class, got %v", got)
	}
}

// A child may only be checked in to a class they are enrolled in — the desk
// offers nothing else. Class History can still put someone into a session
// after the fact to correct a record, and that visit is recorded; there is no
// enrolment for it to charge, and taking the hour off a different class they
// did attend would be worse than taking it off nothing.
func TestAttendanceInAClassTheyAreNotEnrolledInChargesNothing(t *testing.T) {
	c := &client{t: t, srv: newServer(t)}
	c.login("admin@jca.ac.th")
	studentID, _, enrolmentID := desk(t, c, "14:00", "14:30", 14)

	_, other, _ := c.do("POST", "/api/v1/classes", map[string]any{"name": "Someone Else's Class"})
	_, otherSession, _ := c.do("POST", "/api/v1/class-sessions", map[string]any{
		"class_id": other["class_id"], "session_date": "2026-08-21",
		"start_time": "15:00", "end_time": "16:00", "session_status": "Ongoing",
	})

	status, _, _ := c.do("POST", "/api/v1/attendance", map[string]any{
		"student_id": studentID, "session_id": otherSession["session_id"],
		"check_in_time": "2026-08-21T08:00:00Z",
	})
	if status != 201 {
		t.Fatalf("the visit is still a fact: want 201, got %d", status)
	}
	if got := balanceOf(c, enrolmentID); !near(got, 14) {
		t.Fatalf("their own class must not pay for it, got %v", got)
	}
}

// A class is written down with a name and nothing else; class_type is filled in
// rather than demanded.
func TestAClassNeedsOnlyAName(t *testing.T) {
	c := &client{t: t, srv: newServer(t)}
	c.login("admin@jca.ac.th")

	status, created, _ := c.do("POST", "/api/v1/classes", map[string]any{"name": "Saturday Beginners"})
	if status != 201 {
		t.Fatalf("want 201, got %d (%v)", status, created)
	}
	if created["class_type"] != "Group" {
		t.Fatalf("class_type should default rather than be demanded, got %v", created["class_type"])
	}
}

// A package that has been sold cannot be deleted — a payment points at it, and
// the console reads it to say what that payment bought. Retiring it takes it
// off the price list and leaves the receipt adding up.
func TestArchivingAPackageKeepsWhatItSold(t *testing.T) {
	c := &client{t: t, srv: newServer(t)}
	c.login("admin@jca.ac.th")
	_, class, _ := c.do("POST", "/api/v1/classes", map[string]any{"name": "Last Term's Class"})
	_, pkg, _ := c.do("POST", "/api/v1/credit-packages", map[string]any{
		"class_id": class["class_id"], "credit_amount": 20, "standard_price": 12000, "validity_days": 90,
	})
	_, student, _ := c.do("POST", "/api/v1/students", map[string]any{"name": "Bought One"})
	_, payment, _ := c.do("POST", "/api/v1/payments", map[string]any{
		"student_id": student["student_id"], "credit_package_id": pkg["credit_package_id"],
		"amount": 12000, "final_amount": 12000, "payment_method": "Cash",
		"status": "Paid", "payment_date": "2026-08-21",
	})

	// The wall: a sold package cannot be deleted.
	status, _, _ := c.do("DELETE", "/api/v1/credit-packages/"+pkg["credit_package_id"].(string), nil)
	if status == 200 {
		t.Fatalf("a sold package should not be deletable outright")
	}

	status, archived, _ := c.do("PATCH", "/api/v1/credit-packages/"+pkg["credit_package_id"].(string),
		map[string]any{"archived_at": "2026-08-21T10:00:00Z"})
	if status != 200 || archived["archived_at"] == nil {
		t.Fatalf("archive: %d (%v)", status, archived)
	}

	// The receipt still adds up: the payment still finds its package, and the
	// package still says twenty credits.
	_, got, _ := c.do("GET", "/api/v1/credit-packages/"+pkg["credit_package_id"].(string), nil)
	if amount, _ := got["credit_amount"].(float64); !near(amount, 20) {
		t.Fatalf("the package should still say what it sold, got %v", got["credit_amount"])
	}
	_, stillThere, _ := c.do("GET", "/api/v1/payments/"+payment["payment_id"].(string), nil)
	if stillThere["credit_package_id"] != pkg["credit_package_id"] {
		t.Fatalf("the payment lost its package: %v", stillThere)
	}

	status, restored, _ := c.do("PATCH", "/api/v1/credit-packages/"+pkg["credit_package_id"].(string),
		map[string]any{"archived_at": nil})
	if status != 200 || restored["archived_at"] != nil {
		t.Fatalf("restore: %d (%v)", status, restored)
	}
}

// Archiving retires a class without touching what it leaves behind.
func TestArchivingAClassKeepsItsHistory(t *testing.T) {
	c := &client{t: t, srv: newServer(t)}
	c.login("admin@jca.ac.th")
	studentID, sessionID, enrolmentID := desk(t, c, "14:00", "15:00", 10)
	c.do("POST", "/api/v1/attendance", map[string]any{
		"student_id": studentID, "session_id": sessionID, "check_in_time": "2026-08-21T07:00:00Z",
	})

	_, session, _ := c.do("GET", "/api/v1/class-sessions/"+sessionID, nil)
	classID := session["class_id"].(string)

	status, archived, _ := c.do("PATCH", "/api/v1/classes/"+classID,
		map[string]any{"archived_at": "2026-08-21T10:00:00Z"})
	if status != 200 || archived["archived_at"] == nil {
		t.Fatalf("archive: %d (%v)", status, archived)
	}

	// The row is still there to be joined to, so the name survives on
	// everything that referenced it.
	_, got, _ := c.do("GET", "/api/v1/classes/"+classID, nil)
	if got["name"] != "Half Hour Club" {
		t.Fatalf("the class should still name itself, got %v", got)
	}
	if bal := balanceOf(c, enrolmentID); !near(bal, 9) {
		t.Fatalf("the hour attended should still be spent, got %v", bal)
	}

	// And it comes back, which a delete never would.
	status, restored, _ := c.do("PATCH", "/api/v1/classes/"+classID, map[string]any{"archived_at": nil})
	if status != 200 || restored["archived_at"] != nil {
		t.Fatalf("restore: %d (%v)", status, restored)
	}
}

// Credits cannot go below zero. A debt nobody agreed to is not a record of
// anything, and nothing in the console ever collects it — the office tops the
// child up and adds them again.
func TestAnEmptyBalanceRefusesRatherThanGoingNegative(t *testing.T) {
	c := &client{t: t, srv: newServer(t)}
	c.login("admin@jca.ac.th")
	studentID, sessionID, enrolmentID := desk(t, c, "14:00", "15:00", 0)

	status, body, _ := c.do("POST", "/api/v1/attendance", map[string]any{
		"student_id": studentID, "session_id": sessionID, "check_in_time": "2026-08-21T07:00:00Z",
	})
	if status != 400 {
		t.Fatalf("want 400, got %d (%v)", status, body)
	}
	// Said in words the desk can act on, not "check references and uniqueness".
	if msg, _ := body["error"].(string); !strings.Contains(msg, "credits left") {
		t.Fatalf("the refusal should say why: %q", msg)
	}
	if got := balanceOf(c, enrolmentID); !near(got, 0) {
		t.Fatalf("balance should be untouched, got %v", got)
	}

	// And no attendance was left behind: the charge and the row roll back together.
	_, _, rows := c.do("GET", "/api/v1/attendance", nil)
	for _, r := range rows {
		if r["student_id"] == studentID {
			t.Fatalf("a refused check-in must not leave an attendance row: %v", r)
		}
	}
}

// Exactly enough is enough — the floor is zero, not "some to spare".
func TestExactlyEnoughCreditsIsAllowed(t *testing.T) {
	c := &client{t: t, srv: newServer(t)}
	c.login("admin@jca.ac.th")
	studentID, sessionID, enrolmentID := desk(t, c, "14:00", "14:30", 0.5)

	status, body, _ := c.do("POST", "/api/v1/attendance", map[string]any{
		"student_id": studentID, "session_id": sessionID, "check_in_time": "2026-08-21T07:00:00Z",
	})
	if status != 201 {
		t.Fatalf("want 201, got %d (%v)", status, body)
	}
	if got := balanceOf(c, enrolmentID); !near(got, 0) {
		t.Fatalf("want 0, got %v", got)
	}
}

// A student with no enrolment has nowhere to charge. The visit is still a fact.
func TestAttendanceWithoutAnEnrolmentIsStillRecorded(t *testing.T) {
	c := &client{t: t, srv: newServer(t)}
	c.login("admin@jca.ac.th")
	_, class, _ := c.do("POST", "/api/v1/classes", map[string]any{"name": "Drop In", "class_type": "Group"})
	_, session, _ := c.do("POST", "/api/v1/class-sessions", map[string]any{
		"class_id": class["class_id"], "session_date": "2026-08-21",
		"start_time": "14:00", "end_time": "15:00", "session_status": "Ongoing",
	})
	_, student, _ := c.do("POST", "/api/v1/students", map[string]any{"name": "Walk In"})

	status, _, _ := c.do("POST", "/api/v1/attendance", map[string]any{
		"student_id": student["student_id"], "session_id": session["session_id"],
		"check_in_time": "2026-08-21T07:00:00Z",
	})
	if status != 201 {
		t.Fatalf("want 201, got %d", status)
	}
}

// A timetable with no readable length is incomplete, not free — and must not
// hand out credits by charging a negative number of hours.
func TestSessionEndingBeforeItStartsChargesNothing(t *testing.T) {
	c := &client{t: t, srv: newServer(t)}
	c.login("admin@jca.ac.th")
	studentID, sessionID, enrolmentID := desk(t, c, "15:00", "14:00", 5)

	c.do("POST", "/api/v1/attendance", map[string]any{
		"student_id": studentID, "session_id": sessionID, "check_in_time": "2026-08-21T07:00:00Z",
	})
	if got := balanceOf(c, enrolmentID); !near(got, 5) {
		t.Fatalf("balance should be untouched, got %v", got)
	}
}
