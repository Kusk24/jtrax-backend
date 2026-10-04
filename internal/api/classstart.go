// A class made for later: who is booked on it, and what happens when it
// starts.
//
// Attendance is charged the moment it is written (credits.go), so a class's
// roster cannot be its attendance until the class is running — a lesson next
// Saturday would cost the family its credits today. Until the start, the
// roster is bookings, which cost nothing. At the start each booking becomes an
// attendance row through the same rules a check-in at the desk passes —
// cancelled class, one class at a time, expired credits, the negative limit —
// and is charged exactly as one would be. Taking a no-show off the roster
// afterwards refunds them, as it always has.
package api

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/Kusk24/jtrax-backend/internal/academytime"
)

// refuseBooking keeps a booking to a class that has not started, has not
// been called off, and does not overlap another class the student is in or
// booked on that day.
func refuseBooking(tx *sql.Tx, bookingID string) error {
	var day, start, end string
	var cancelled sql.NullString
	err := tx.QueryRow(`
		SELECT s.session_date, s.start_time, s.end_time, s.cancelled_at
		  FROM session_booking b JOIN class_session s ON s.session_id = b.session_id
		 WHERE b.booking_id = ?`, bookingID).Scan(&day, &start, &end, &cancelled)
	if err != nil {
		return err
	}
	if cancelled.Valid {
		return &ClientError{Status: http.StatusConflict, Message: "this class has been cancelled"}
	}
	if begins, ok := academytime.At(day, start); ok && !academytime.Now().Before(begins) {
		return &ClientError{Status: http.StatusConflict,
			Message: "this class has already started — check the student in instead"}
	}
	var other string
	err = tx.QueryRow(`
		SELECT c.name FROM session_booking mine
		  JOIN class_session here ON here.session_id = mine.session_id
		  JOIN class_session there ON there.session_date = here.session_date
		                          AND there.session_id <> here.session_id
		                          AND there.cancelled_at IS NULL
		                          AND there.start_time < here.end_time
		                          AND here.start_time < there.end_time
		  JOIN class c ON c.class_id = there.class_id
		 WHERE mine.booking_id = ?
		   AND (EXISTS (SELECT 1 FROM session_booking b
		                 WHERE b.session_id = there.session_id AND b.student_id = mine.student_id)
		     OR EXISTS (SELECT 1 FROM attendance a
		                 WHERE a.session_id = there.session_id AND a.student_id = mine.student_id
		                   AND COALESCE(a.check_out_time, '') = ''))
		 LIMIT 1`, bookingID).Scan(&other)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return &ClientError{Status: http.StatusConflict, Message: "this student is already in " + other + " at that time"}
}

// attendanceAfterInsert is the attendance rules plus one: a student checked
// in to a class they were booked on — early, by hand — is no longer booked,
// so the start does not check them in a second time.
func attendanceAfterInsert(tx *sql.Tx, attendanceID string) error {
	if err := refuseClashingAttendance(tx, attendanceID); err != nil {
		return err
	}
	_, err := tx.Exec(`
		DELETE FROM session_booking
		 WHERE (session_id, student_id) IN (
		       SELECT session_id, student_id FROM attendance WHERE attendance_id = ?)`, attendanceID)
	return err
}

// dropSessionBookings clears a session's bookings before it is deleted.
func dropSessionBookings(tx *sql.Tx, sessionID string) error {
	_, err := tx.Exec(`DELETE FROM session_booking WHERE session_id = ?`, sessionID)
	return err
}

// startEvery is how often the timer looks for classes that have started. A
// class's start is to the minute, so is this.
const startEvery = time.Minute

// StartClassStarts runs RunClassStarts every minute until ctx ends. Started
// by the server, not by NewHandler, so tests control when it runs.
func StartClassStarts(ctx context.Context, d *sql.DB) {
	go func() {
		ticker := time.NewTicker(startEvery)
		defer ticker.Stop()
		for {
			if err := RunClassStarts(d, academytime.Now()); err != nil {
				log.Printf("class starts: %v", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

// RunClassStarts checks in everyone booked on a class that has started by
// `now`, and marks a Scheduled class that has started as Ongoing.
func RunClassStarts(d *sql.DB, now time.Time) error {
	now = now.In(academytime.Location())
	day, clock := now.Format("2006-01-02"), now.Format("15:04")
	started := `s.cancelled_at IS NULL
	            AND (s.session_date < ? OR (s.session_date = ? AND s.start_time <= ?))`

	if _, err := d.Exec(`UPDATE class_session AS s SET session_status = 'Ongoing'
	                      WHERE s.session_status = 'Scheduled' AND `+started, day, day, clock); err != nil {
		return err
	}

	rows, err := d.Query(`
		SELECT b.booking_id, b.session_id, b.student_id, s.session_date, s.start_time
		  FROM session_booking b JOIN class_session s ON s.session_id = b.session_id
		 WHERE b.failed_reason IS NULL AND `+started, day, day, clock)
	if err != nil {
		return err
	}
	type due struct{ booking, session, student, day, start string }
	var list []due
	for rows.Next() {
		var x due
		if err := rows.Scan(&x.booking, &x.session, &x.student, &x.day, &x.start); err != nil {
			rows.Close()
			return err
		}
		list = append(list, x)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, x := range list {
		if err := startBooking(d, x.booking, x.session, x.student, x.day, x.start); err != nil {
			log.Printf("class starts: booking %s: %v", x.booking, err)
		}
	}
	return nil
}

// startBooking turns one booking into attendance, checked in at the class's
// start, and charges it. A rule that refuses it — expired credits, the
// negative limit, a clash — leaves the booking with the reason instead.
func startBooking(d *sql.DB, bookingID, sessionID, studentID, day, start string) error {
	checkIn := time.Now().UTC()
	if t, ok := academytime.At(day, start); ok {
		checkIn = t.UTC()
	}
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	attendanceID := newID("att")
	stamp := sqliteNow()
	if _, err := tx.Exec(`
		INSERT INTO attendance (attendance_id, student_id, session_id, check_in_time, created_at, updated_at)
		VALUES (?,?,?,?,?,?)`,
		attendanceID, studentID, sessionID, checkIn.Format("2006-01-02T15:04:05.000Z"), stamp, stamp); err != nil {
		return err
	}
	err = attendanceAfterInsert(tx, attendanceID)
	if err == nil {
		err = chargeAttendance(tx, attendanceID)
	}
	var refused *ClientError
	if errors.As(err, &refused) {
		tx.Rollback()
		_, uerr := d.Exec(`UPDATE session_booking SET failed_reason = ? WHERE booking_id = ?`, refused.Message, bookingID)
		return uerr
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}
