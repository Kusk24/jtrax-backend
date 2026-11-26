package families

import (
	"database/sql"
	"net/http"

	"github.com/Kusk24/jtrax-backend/internal/records"
)

// StudentAfterWrite drops an archived student's pending bookings, so a class
// that starts later does not check them in.
func StudentAfterWrite(tx *sql.Tx, studentID string) error {
	_, err := tx.Exec(`
		DELETE FROM session_booking
		 WHERE student_id = ?
		   AND EXISTS (SELECT 1 FROM student s
		                WHERE s.student_id = ? AND COALESCE(s.archived_at, '') <> '')`,
		studentID, studentID)
	return err
}

// RefuseArchivedAttendance stops an archived student being checked in.
func RefuseArchivedAttendance(tx *sql.Tx, attendanceID string) error {
	var archived bool
	err := tx.QueryRow(`
		SELECT COALESCE(s.archived_at, '') <> ''
		  FROM attendance a JOIN student s ON s.student_id = a.student_id
		 WHERE a.attendance_id = ?`, attendanceID).Scan(&archived)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if archived {
		return &records.ClientError{Status: http.StatusConflict, Message: "this student is archived"}
	}
	return nil
}
