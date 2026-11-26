// Package payments implements the school’s payment workflows.
package payments

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/Kusk24/jtrax-backend/internal/academytime"
	"github.com/Kusk24/jtrax-backend/internal/auth"
	"github.com/Kusk24/jtrax-backend/internal/db"
)

var (
	ErrNotFound      = errors.New("no such payment")
	ErrAlreadyVoided = errors.New("this payment is already voided")
)

// VoidPayment voids an administrator’s validated payment correction and returns
// the signed credit adjustment. The payment, ledger, enrolment, and audit row
// are committed together.
func VoidPayment(d *sql.DB, caller *auth.Identity, paymentID, reason, note string) (float64, error) {
	tx, err := d.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var status, enrollmentID string
	err = tx.QueryRow(`SELECT status, COALESCE(enrollment_id, '') FROM payment WHERE payment_id = ?`, paymentID).
		Scan(&status, &enrollmentID)
	if err == sql.ErrNoRows {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, err
	}
	if status == "Voided" {
		return 0, ErrAlreadyVoided
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := tx.Exec(`UPDATE payment SET status = 'Voided', voided_at = ?, voided_by = ?, void_reason = ?,
                             stripe_checkout_url = NULL
                       WHERE payment_id = ?`,
		now, caller.UserAccountID, voidReasonText(reason, note), paymentID); err != nil {
		return 0, err
	}
	reversed, err := reverseVoidedCredits(tx, paymentID, reason)
	if err != nil {
		return 0, err
	}
	ended, err := endEnrolmentOfVoided(tx, paymentID, enrollmentID)
	if err != nil {
		return 0, err
	}

	changes := map[string]any{"status": map[string]any{"from": status, "to": "Voided"}}
	if reversed != 0 {
		changes["credits_reversed"] = reversed
	}
	if ended {
		changes["enrollment_ended"] = enrollmentID
	}
	if note != "" {
		changes["note"] = note
	}
	raw, _ := json.Marshal(changes)
	_, err = tx.Exec(`INSERT INTO payment_audit (audit_id, payment_id, action, changed_by, changed_by_name, changed_at, changes, reason)
                  VALUES (?,?,?,?,?,?,?,?)`,
		db.NewID("pau"), paymentID, "void", caller.UserAccountID, caller.DisplayName, now, string(raw), reason)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return reversed, nil
}

// voidReasonText is the reason as stored on the payment: the code, and the
// admin's words for "other".
func voidReasonText(reason, note string) string {
	if note == "" {
		return reason
	}
	return reason + ": " + note
}

// reverseVoidedCredits takes back what is left of the credits the payment
// bought, with an adjustment linked to it. A balance already spent may go
// below zero: that is what the record now says happened.
func reverseVoidedCredits(tx *sql.Tx, paymentID, reason string) (float64, error) {
	var net float64
	var enrollmentID, studentID, classID sql.NullString
	err := tx.QueryRow(`
		SELECT COALESCE(SUM(amount), 0),
		       (SELECT enrollment_id FROM credit_transaction WHERE payment_id = ? AND transaction_type = 'purchase' LIMIT 1),
		       (SELECT student_id FROM credit_transaction WHERE payment_id = ? AND transaction_type = 'purchase' LIMIT 1),
		       (SELECT class_id FROM credit_transaction WHERE payment_id = ? AND transaction_type = 'purchase' LIMIT 1)
		  FROM credit_transaction WHERE payment_id = ?`, paymentID, paymentID, paymentID, paymentID).
		Scan(&net, &enrollmentID, &studentID, &classID)
	if err != nil || net <= 0 {
		return 0, err
	}
	_, err = tx.Exec(`INSERT INTO credit_transaction
	                    (credit_transaction_id, enrollment_id, student_id, class_id, transaction_type,
	                     amount, transaction_date, payment_id, notes)
	                  VALUES (?,?,?,?, 'manual_adjustment', ?,?,?,?)`,
		db.NewID("ctx"), enrollmentID, studentID, classID, -net, academytime.Today(), paymentID,
		"Payment voided ("+reason+")")
	return -net, err
}

// endEnrolmentOfVoided ends the enrolment the payment opened, when it has
// nothing else behind it — no class attended, no other credits, no other
// payment. An enrolment with any history of its own is left alone.
func endEnrolmentOfVoided(tx *sql.Tx, paymentID, enrollmentID string) (bool, error) {
	if enrollmentID == "" {
		return false, nil
	}
	var others int
	err := tx.QueryRow(`
		SELECT (SELECT COUNT(*) FROM credit_transaction
		         WHERE enrollment_id = ? AND COALESCE(payment_id, '') <> ?)
		     + (SELECT COUNT(*) FROM payment
		         WHERE enrollment_id = ? AND payment_id <> ? AND status <> 'Voided')
		     + (SELECT COUNT(*) FROM attendance a
		          JOIN class_session s ON s.session_id = a.session_id
		          JOIN student_enrollment e ON e.class_id = s.class_id AND e.student_id = a.student_id
		         WHERE e.enrollment_id = ?)`,
		enrollmentID, paymentID, enrollmentID, paymentID, enrollmentID).Scan(&others)
	if err != nil || others > 0 {
		return false, err
	}
	res, err := tx.Exec(`UPDATE student_enrollment SET status = 'Withdrawn', ended_date = ?
	                      WHERE enrollment_id = ? AND status = 'Active'`, academytime.Today(), enrollmentID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}
