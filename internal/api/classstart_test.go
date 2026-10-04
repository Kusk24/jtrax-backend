package api_test

/* A class made for later: students are booked on it for nothing, and their
   credits are spent when it starts — by the same rule as a check-in. */

import (
	"database/sql"
	"testing"
	"time"

	"github.com/Kusk24/jtrax-backend/internal/api"
)

var bangkok = time.FixedZone("ICT", 7*60*60)

// scheduled sets up a class on `date` from 10:00 to 11:00, one student
// enrolled in it with `opening` credits, and returns the pieces.
func scheduled(t *testing.T, date string, opening float64) (c *client, d *sql.DB, studentID, sessionID, enrolmentID string) {
	t.Helper()
	d = newDB(t)
	c = &client{t: t, srv: newServerOn(t, d)}
	c.login("admin@jca.ac.th")
	_, class, _ := c.do("POST", "/api/v1/classes", map[string]any{"name": "King Slayer", "class_type": "Group"})
	status, session, _ := c.do("POST", "/api/v1/class-sessions", map[string]any{
		"class_id": class["class_id"], "session_date": date,
		"start_time": "10:00", "end_time": "11:00", "session_status": "Scheduled",
	})
	if status != 201 {
		t.Fatalf("schedule: %d (%v)", status, session)
	}
	_, student, _ := c.do("POST", "/api/v1/students", map[string]any{"name": "Booked Child"})
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
	return c, d, student["student_id"].(string), session["session_id"].(string), enrolment["enrollment_id"].(string)
}

func book(t *testing.T, c *client, studentID, sessionID string) (int, map[string]any) {
	t.Helper()
	status, row, _ := c.do("POST", "/api/v1/session-bookings", map[string]any{
		"student_id": studentID, "session_id": sessionID,
	})
	return status, row
}

func at10(date string, minute int) time.Time {
	d, _ := time.ParseInLocation("2006-01-02", date, bangkok)
	return d.Add(10*time.Hour + time.Duration(minute)*time.Minute)
}

func attendanceCount(d *sql.DB, sessionID string) int {
	var n int
	d.QueryRow(`SELECT COUNT(*) FROM attendance WHERE session_id = ?`, sessionID).Scan(&n)
	return n
}

func bookingCount(d *sql.DB, sessionID string) int {
	var n int
	d.QueryRow(`SELECT COUNT(*) FROM session_booking WHERE session_id = ?`, sessionID).Scan(&n)
	return n
}

func TestBookingAFutureClassSpendsNothingUntilItStarts(t *testing.T) {
	date := day(6)
	c, d, studentID, sessionID, enrolmentID := scheduled(t, date, 10)

	if status, row := book(t, c, studentID, sessionID); status != 201 {
		t.Fatalf("book: %d (%v)", status, row)
	}
	if got := balanceOf(c, enrolmentID); !near(got, 10) {
		t.Fatalf("booking spent credits: balance %v, want 10", got)
	}

	// A minute before the start: still booked, still free.
	if err := api.RunClassStarts(d, at10(date, -1)); err != nil {
		t.Fatal(err)
	}
	if attendanceCount(d, sessionID) != 0 || !near(balanceOf(c, enrolmentID), 10) {
		t.Fatal("the student was checked in before the class started")
	}

	// The start: checked in, charged one hour, the booking gone.
	api.RunClassStarts(d, at10(date, 0))
	if attendanceCount(d, sessionID) != 1 || bookingCount(d, sessionID) != 0 {
		t.Fatalf("at the start: %d attendance, %d bookings; want 1 and 0",
			attendanceCount(d, sessionID), bookingCount(d, sessionID))
	}
	if got := balanceOf(c, enrolmentID); !near(got, 9) {
		t.Fatalf("at the start the hour is spent: balance %v, want 9", got)
	}
	var status string
	d.QueryRow(`SELECT session_status FROM class_session WHERE session_id = ?`, sessionID).Scan(&status)
	if status != "Ongoing" {
		t.Fatalf("a started class reads %q, want Ongoing", status)
	}

	// Later passes charge nothing more.
	api.RunClassStarts(d, at10(date, 30))
	if got := balanceOf(c, enrolmentID); !near(got, 9) {
		t.Fatalf("charged twice: balance %v", got)
	}
}

func TestCancellingAScheduledClassRefundsNothingBecauseNothingWasSpent(t *testing.T) {
	date := day(6)
	c, d, studentID, sessionID, enrolmentID := scheduled(t, date, 10)
	book(t, c, studentID, sessionID)

	if status, out, _ := c.do("POST", "/api/v1/class-sessions/"+sessionID+"/cancel", nil); status != 200 {
		t.Fatalf("cancel: %d (%v)", status, out)
	}
	if bookingCount(d, sessionID) != 0 {
		t.Fatal("the cancelled class kept its bookings")
	}
	api.RunClassStarts(d, at10(date, 5))
	if attendanceCount(d, sessionID) != 0 || !near(balanceOf(c, enrolmentID), 10) {
		t.Fatal("a cancelled class checked its students in at the start")
	}
}

func TestCheckingInEarlyByHandChargesOnce(t *testing.T) {
	date := day(6)
	c, d, studentID, sessionID, enrolmentID := scheduled(t, date, 10)
	book(t, c, studentID, sessionID)

	// The teacher starts with the child before 10:00.
	if status, row, _ := c.do("POST", "/api/v1/attendance", map[string]any{
		"student_id": studentID, "session_id": sessionID, "check_in_time": time.Now().UTC().Format(time.RFC3339),
	}); status != 201 {
		t.Fatalf("early check-in: %d (%v)", status, row)
	}
	if bookingCount(d, sessionID) != 0 {
		t.Fatal("a student checked in by hand is still booked")
	}
	api.RunClassStarts(d, at10(date, 1))
	if attendanceCount(d, sessionID) != 1 || !near(balanceOf(c, enrolmentID), 9) {
		t.Fatalf("want one attendance and one hour spent, got %d and balance %v",
			attendanceCount(d, sessionID), balanceOf(c, enrolmentID))
	}
}

func TestRescheduledLaterTheBookingWaitsForTheNewTime(t *testing.T) {
	date := day(6)
	c, d, studentID, sessionID, _ := scheduled(t, date, 10)
	book(t, c, studentID, sessionID)
	c.do("PATCH", "/api/v1/class-sessions/"+sessionID, map[string]any{"start_time": "14:00", "end_time": "15:00"})

	api.RunClassStarts(d, at10(date, 5)) // the old start
	if attendanceCount(d, sessionID) != 0 {
		t.Fatal("checked in at the old time")
	}
	api.RunClassStarts(d, at10(date, 4*60))
	if attendanceCount(d, sessionID) != 1 {
		t.Fatal("not checked in at the new time")
	}
}

func TestAClassThatHasStartedTakesCheckInsNotBookings(t *testing.T) {
	c, _, studentID, sessionID, _ := scheduled(t, day(-1), 10)
	if status, _ := book(t, c, studentID, sessionID); status != 409 {
		t.Fatalf("booking a class that has started: %d, want 409", status)
	}
}

func TestABookingThatCannotBeChargedStaysWithTheReason(t *testing.T) {
	date := day(6)
	c, d, studentID, sessionID, enrolmentID := scheduled(t, date, 0) // no credits
	if status, row := book(t, c, studentID, sessionID); status != 201 {
		t.Fatalf("book: %d (%v)", status, row)
	}
	api.RunClassStarts(d, at10(date, 0))
	var reason sql.NullString
	d.QueryRow(`SELECT failed_reason FROM session_booking WHERE session_id = ?`, sessionID).Scan(&reason)
	if attendanceCount(d, sessionID) != 0 || !reason.Valid || reason.String == "" {
		t.Fatalf("want the booking kept with a reason and no attendance; got %d attendance, reason %v",
			attendanceCount(d, sessionID), reason)
	}
	if got := balanceOf(c, enrolmentID); !near(got, 0) {
		t.Fatalf("a refused start touched the balance: %v", got)
	}
}

func TestOneClassAtATimeHoldsForBookingsToo(t *testing.T) {
	date := day(6)
	c, _, studentID, sessionID, _ := scheduled(t, date, 10)
	book(t, c, studentID, sessionID)
	_, class, _ := c.do("POST", "/api/v1/classes", map[string]any{"name": "Summer Challenger", "class_type": "Group"})
	_, other, _ := c.do("POST", "/api/v1/class-sessions", map[string]any{
		"class_id": class["class_id"], "session_date": date,
		"start_time": "10:30", "end_time": "11:30", "session_status": "Scheduled",
	})
	if status, _ := book(t, c, studentID, other["session_id"].(string)); status != 409 {
		t.Fatalf("booking an overlapping class: %d, want 409", status)
	}
}

// The charge names the student, as a purchase does, so the parent's
// "Credits used" (which reads the ledger by student) counts it.
func TestAClassChargeNamesItsStudent(t *testing.T) {
	date := day(6)
	c, d, studentID, sessionID, enrolmentID := scheduled(t, date, 10)
	book(t, c, studentID, sessionID)
	api.RunClassStarts(d, at10(date, 0))
	var who sql.NullString
	if err := d.QueryRow(`SELECT student_id FROM credit_transaction
	                       WHERE transaction_type = 'consumption' AND enrollment_id = ?`, enrolmentID).Scan(&who); err != nil {
		t.Fatal(err)
	}
	if who.String != studentID {
		t.Fatalf("charge names %q, want %q", who.String, studentID)
	}
}
