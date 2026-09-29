// One student, one class at a time.
//
// A child cannot sit in two classes whose hours overlap — King Slayer from
// 10:00 to 12:00 and Summer Challenger from 11:00 to 12:00 on the same day.
// The console already shows the clash and will not let the desk tick the
// child, but the teacher's roster and Class History write attendance too, so
// the rule lives here where every one of them passes through.
package api

import (
	"database/sql"
	"fmt"
	"net/http"
)

// refuseClashingAttendance rejects a new attendance row when the same student
// is already in another session on the same day whose hours overlap this one.
// Sessions that only touch — one ends at 12:00, the next starts at 12:00 — do
// not clash. A cancelled class is deleted along with its attendance, so it
// never counts, and neither does a class the student has already checked out
// of: it is over for them.
func refuseClashingAttendance(tx *sql.Tx, attendanceID string) error {
	// Nobody joins a class that was called off.
	var cancelled sql.NullString
	if err := tx.QueryRow(`
		SELECT s.cancelled_at FROM attendance a
		JOIN class_session s ON s.session_id = a.session_id
		WHERE a.attendance_id = ?`, attendanceID).Scan(&cancelled); err != nil && err != sql.ErrNoRows {
		return err
	}
	if cancelled.Valid {
		return &ClientError{Status: http.StatusConflict, Message: "this class has been cancelled"}
	}

	var className string
	err := tx.QueryRow(`
		SELECT c.name
		FROM attendance mine
		JOIN class_session here  ON here.session_id = mine.session_id
		JOIN attendance other    ON other.student_id = mine.student_id
		                        AND other.attendance_id <> mine.attendance_id
		                        -- Checked out: that class is over for them.
		                        AND COALESCE(other.check_out_time, '') = ''
		JOIN class_session there ON there.session_id = other.session_id
		JOIN class c             ON c.class_id = there.class_id
		WHERE mine.attendance_id = ?
		  AND there.session_id <> here.session_id
		  AND there.session_date = here.session_date
		  AND there.cancelled_at IS NULL
		  AND there.start_time < here.end_time
		  AND here.start_time < there.end_time
		ORDER BY there.start_time
		LIMIT 1`, attendanceID).Scan(&className)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	return &ClientError{
		Status:  http.StatusConflict,
		Message: fmt.Sprintf("this student is already in %s at that time", className),
	}
}
