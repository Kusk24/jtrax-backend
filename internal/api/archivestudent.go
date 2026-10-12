package api

import (
	"database/sql"
	"net/http"
)

// An archived student has left the academy. Everything they did is kept, but
// they are out of the day-to-day: no sign-in (auth.Lookup), no check-in, no
// bookings and no credit reminders.

// studentAfterWrite drops an archived student's pending bookings, so a class
// that starts later does not check them in.
func studentAfterWrite(tx *sql.Tx, studentID string) error {
	_, err := tx.Exec(`
		DELETE FROM session_booking
		 WHERE student_id = ?
		   AND EXISTS (SELECT 1 FROM student s
		                WHERE s.student_id = ? AND COALESCE(s.archived_at, '') <> '')`,
		studentID, studentID)
	return err
}

// refuseArchivedAttendance stops an archived student being checked in.
func refuseArchivedAttendance(tx *sql.Tx, attendanceID string) error {
	var archived bool
	err := tx.QueryRow(`
		SELECT COALESCE(s.archived_at, '') <> ''
		  FROM attendance a JOIN student s ON s.student_id = a.student_id
		 WHERE a.attendance_id = ?`, attendanceID).Scan(&archived)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if archived {
		return &ClientError{Status: http.StatusConflict, Message: "this student is archived"}
	}
	return nil
}
