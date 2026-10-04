// Attendance spends credits.
//
// One credit is one hour, so a session is worth what it lasts: a ninety-minute
// class costs 1.5 and a half-hour costs 0.5. `credit_transaction.amount` and
// `class_session.duration_hours` are both REAL, and the balance is a plain sum
// over the ledger, so the fractions carry all the way to the chip on screen.
//
// Nothing used to write this. The schema said what was meant — a
// `consumption` type, an `attendance_id` on the transaction, a seed row
// showing the shape — and no code path in any of the four apps created one, so
// checking a child in never touched their balance. It is done here rather than
// in the console because the front desk, the teacher's roster and Class
// History all write the same attendance rows, and a rule kept in one of three
// clients is a rule two clients get wrong.
package api

import (
	"database/sql"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Kusk24/jtrax-backend/internal/academytime"
)

// hoursBetween returns the length of "HH:MM" → "HH:MM" in hours, or 0 when
// either end is unreadable. A session that ends before it starts is 0 rather
// than negative: a nonsense timetable must not hand out credits.
func hoursBetween(start, end string) float64 {
	s, okS := minutesOfDay(start)
	e, okE := minutesOfDay(end)
	if !okS || !okE || e <= s {
		return 0
	}
	return float64(e-s) / 60
}

func minutesOfDay(clock string) (int, bool) {
	parts := strings.Split(strings.TrimSpace(clock), ":")
	if len(parts) < 2 {
		return 0, false
	}
	h, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, false
	}
	m, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, false
	}
	if h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, false
	}
	return h*60 + m, true
}

// sessionHours is what one attendance at this session costs: the stored
// duration when there is one, otherwise the clock. Console-created sessions
// have never set duration_hours — the form collects a start and an end and
// nothing worked out the difference — so the fallback is the normal path for
// anything staff made, not an edge case.
func sessionHours(tx *sql.Tx, sessionID string) (hours float64, date, start string, err error) {
	var duration sql.NullFloat64
	var startCol, end, dateCol sql.NullString
	err = tx.QueryRow(
		`SELECT duration_hours, start_time, end_time, session_date FROM class_session WHERE session_id = ?`,
		sessionID).Scan(&duration, &startCol, &end, &dateCol)
	if err != nil {
		return 0, "", "", err
	}
	if duration.Valid && duration.Float64 > 0 {
		return duration.Float64, dateCol.String, startCol.String, nil
	}
	return hoursBetween(startCol.String, end.String), dateCol.String, startCol.String, nil
}

// attendedHours is what a visit that ended before the class did costs: from
// when the student arrived (or the class began, if they were early) to when
// they left, rounded to the academy's step. A visit with no check-out, or one
// that ran to the scheduled end or past it, costs the whole class — `hours`.
func attendedHours(tx *sql.Tx, hours float64, date, start, checkIn, checkOut string) float64 {
	out, ok := academytime.Moment(checkOut)
	if !ok {
		return hours
	}
	begin, ok := academytime.At(date, start)
	if !ok {
		return hours
	}
	end := begin.Add(time.Duration(hours * float64(time.Hour)))
	if !out.Before(end) {
		return hours
	}
	from := begin
	if in, ok := academytime.Moment(checkIn); ok && in.After(begin) {
		from = in
	}
	minutes := out.Sub(from).Minutes()
	if minutes <= 0 {
		return 0
	}
	if step := checkoutRoundMinutes(tx); step > 0 {
		minutes = math.Round(minutes/step) * step
	}
	// Rounding up must never cost more than staying to the end would have.
	return math.Min(minutes/60, hours)
}

// enrolmentForCharge picks the enrolment the credits come off: the student's
// place in the class this session belongs to, preferring an active one.
//
// Credits hang off an enrolment, so a student with none has nowhere to charge.
// That is a real state — a walk-in, or a child whose enrolment lapsed — and it
// must not stop the attendance being recorded, so it returns "" and the caller
// records the visit without charging for it.
func enrolmentForCharge(tx *sql.Tx, studentID, sessionID string) string {
	var enrolment string
	err := tx.QueryRow(`
		SELECT e.enrollment_id FROM student_enrollment e
		JOIN class_session s ON s.class_id = e.class_id
		WHERE e.student_id = ? AND s.session_id = ? AND e.deleted_date IS NULL
		ORDER BY CASE e.status WHEN 'Active' THEN 0 ELSE 1 END
		LIMIT 1`, studentID, sessionID).Scan(&enrolment)
	if err == nil {
		return enrolment
	}
	// No enrolment in this class. The desk will not offer a class a child is
	// not enrolled in, so this is a correction made elsewhere — Class History
	// putting someone into a session after the fact. The visit is recorded;
	// there is no enrolment whose balance it could come off, and guessing at
	// another one would take credits from a class they did attend.
	return ""
}

// chargeAttendance writes what this attendance costs, replacing whatever it
// cost before.
//
// Re-runnable on purpose: it is the hook for both the insert and the update,
// and moving a student from a one-hour class to a ninety-minute one has to
// change the charge rather than add a second one.
func chargeAttendance(tx *sql.Tx, attendanceID string) error {
	deleted, err := tx.Exec(
		`DELETE FROM credit_transaction WHERE attendance_id = ? AND transaction_type = 'consumption'`,
		attendanceID)
	if err != nil {
		return err
	}
	// Whether this attendance was ever charged before this call — a fresh
	// check-in vs. a recharge (a check-out being stamped, a session's length
	// changing). The negative-balance and expiry rules below are about being
	// let INTO a class, so they only apply the first time; refusing to
	// recompute an existing charge would block check-out or a length change
	// over a limit that has nothing to do with either.
	priorRows, err := deleted.RowsAffected()
	if err != nil {
		return err
	}
	firstCharge := priorRows == 0

	var studentID, sessionID string
	var checkIn, checkOut sql.NullString
	if err := tx.QueryRow(
		`SELECT student_id, session_id, check_in_time, check_out_time FROM attendance WHERE attendance_id = ?`,
		attendanceID).Scan(&studentID, &sessionID, &checkIn, &checkOut); err != nil {
		return err
	}

	scheduled, date, start, err := sessionHours(tx, sessionID)
	if err != nil {
		return err
	}
	// A session with no readable length is not a free class, it is an
	// incomplete timetable — charging 0 says so quietly and leaves the balance
	// alone until someone fixes the times.
	if scheduled <= 0 {
		return nil
	}
	enrolment := enrolmentForCharge(tx, studentID, sessionID)
	if enrolment == "" {
		return nil
	}
	hours := attendedHours(tx, scheduled, date, start, checkIn.String, checkOut.String)
	if hours <= 0 {
		return nil
	}

	if firstCharge {
		if expiry, err := enrolmentExpiry(tx, enrolment); err != nil {
			return err
		} else if expiry != "" && expiry < date {
			return &ClientError{Status: http.StatusConflict, Message: "this student's credits have expired"}
		}
		balance, err := enrolmentBalance(tx, enrolment)
		if err != nil {
			return err
		}
		if balance-hours < -maxNegativeCredit(tx) {
			return &ClientError{Status: http.StatusConflict, Message: "this would exceed the allowed negative credit balance"}
		}
	}

	// Dated to the day the class ran, not to now: attendance is corrected
	// weeks later on Class History, and the ledger should read as the term
	// happened.
	_, err = tx.Exec(`
		INSERT INTO credit_transaction
			(credit_transaction_id, enrollment_id, student_id, class_id,
			 transaction_type, amount, transaction_date, attendance_id)
		SELECT ?, e.enrollment_id, e.student_id, e.class_id, 'consumption', ?, ?, ?
		  FROM student_enrollment e WHERE e.enrollment_id = ?`,
		newID("ctx"), -hours, date, attendanceID, enrolment)
	return err
}

// enrolmentBalance sums an enrolment's ledger — the same running total the
// console's own balance chip shows.
func enrolmentBalance(tx *sql.Tx, enrolmentID string) (float64, error) {
	var balance float64
	err := tx.QueryRow(
		`SELECT COALESCE(SUM(amount),0) FROM credit_transaction WHERE enrollment_id = ?`,
		enrolmentID).Scan(&balance)
	return balance, err
}

// enrolmentExpiry is the latest expiry_date among an enrolment's credit
// transactions — the same "good until" reading the console's own expiryOf()
// gives the enrolment card and Change Course. Empty means the credits behind
// it never expire.
func enrolmentExpiry(tx *sql.Tx, enrolmentID string) (string, error) {
	var expiry sql.NullString
	err := tx.QueryRow(
		`SELECT MAX(expiry_date) FROM credit_transaction WHERE enrollment_id = ? AND expiry_date IS NOT NULL`,
		enrolmentID).Scan(&expiry)
	return expiry.String, err
}

// maxNegativeCredit is how far a check-in may push a balance below zero
// before it is refused — the same `credit_rule_max_negative` the console's
// Settings screen edits. Default 0: no negative balance until an admin says
// otherwise.
func maxNegativeCredit(tx *sql.Tx) float64 {
	return configNumber(tx, "credit_rule_max_negative", 0)
}

// checkoutRoundMinutes is the step an early check-out's time is rounded to —
// `credit_rule_checkout_round_minutes`, edited on the console's Settings
// screen. Default 15; 0 charges the exact minutes.
func checkoutRoundMinutes(tx *sql.Tx) float64 {
	return configNumber(tx, "credit_rule_checkout_round_minutes", 15)
}

// configNumber reads one non-negative numeric rule from system_configuration,
// falling back to def when it is absent or unreadable.
func configNumber(tx *sql.Tx, key string, def float64) float64 {
	var raw string
	if err := tx.QueryRow(
		`SELECT config_value FROM system_configuration WHERE config_key = ?`, key,
	).Scan(&raw); err == nil {
		if v, err := strconv.ParseFloat(raw, 64); err == nil && v >= 0 {
			return v
		}
	}
	return def
}

// refundAttendance gives back what an attendance cost, for a check-in that
// should not have happened. The balance has to survive the correction.
func refundAttendance(tx *sql.Tx, attendanceID string) error {
	_, err := tx.Exec(
		`DELETE FROM credit_transaction WHERE attendance_id = ? AND transaction_type = 'consumption'`,
		attendanceID)
	return err
}

// storeSessionHours keeps duration_hours true to the clock, so the column is
// worth reading. The console's session form collects a start and an end and
// never filled this in; every session staff created carried a NULL length.
func storeSessionHours(tx *sql.Tx, sessionID string) error {
	var start, end sql.NullString
	if err := tx.QueryRow(
		`SELECT start_time, end_time FROM class_session WHERE session_id = ?`,
		sessionID).Scan(&start, &end); err != nil {
		return err
	}
	hours := hoursBetween(start.String, end.String)
	if hours <= 0 {
		return nil
	}
	if _, err := tx.Exec(
		`UPDATE class_session SET duration_hours = ? WHERE session_id = ?`, hours, sessionID); err != nil {
		return err
	}
	// The length changed, so what every student at it paid changed with it.
	rows, err := tx.Query(`SELECT attendance_id FROM attendance WHERE session_id = ?`, sessionID)
	if err != nil {
		return err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range ids {
		if err := chargeAttendance(tx, id); err != nil {
			return fmt.Errorf("recharging %s: %w", id, err)
		}
	}
	return nil
}
