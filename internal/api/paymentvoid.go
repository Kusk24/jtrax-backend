// Payments are corrected or voided, never deleted.
//
// An edit is recorded in payment_audit — each field it changed, from and to,
// who, and when. A void is for a record made in error: an admin gives a
// reason, the payment becomes Voided (not revenue, and not a refund — no
// money moved back), the credits it bought are taken back by an adjustment
// that points at it, and an enrolment that existed only because of it is
// ended. The original values stay on the row and in the history.
package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/Kusk24/jtrax-backend/internal/auth"
	"github.com/Kusk24/jtrax-backend/internal/httpx"
)

// voidReasons are the reasons a payment can be voided for.
var voidReasons = []string{"duplicate", "wrong_payer", "other"}

// minVoidNote is the shortest explanation accepted for "other".
const minVoidNote = 5

func auditValue(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}

// auditPaymentEdit refuses an edit to a voided payment and records what an
// edit changes. Runs in the edit's transaction.
func auditPaymentEdit(tx *sql.Tx, caller *auth.Identity, paymentID string, existing, patch map[string]any) error {
	if auditValue(existing["status"]) == "Voided" {
		return &ClientError{Status: http.StatusConflict, Message: "a voided payment can't be edited"}
	}
	changes := map[string]map[string]any{}
	for field, v := range patch {
		if auditValue(existing[field]) != auditValue(v) {
			changes[field] = map[string]any{"from": existing[field], "to": v}
		}
	}
	if len(changes) == 0 {
		return nil
	}
	raw, _ := json.Marshal(changes)
	_, err := tx.Exec(`INSERT INTO payment_audit (audit_id, payment_id, action, changed_by, changed_by_name, changed_at, changes)
	                   VALUES (?,?,?,?,?,?,?)`,
		newID("pau"), paymentID, "edit", caller.UserAccountID, caller.DisplayName,
		time.Now().UTC().Format(time.RFC3339), string(raw))
	return err
}

// handleVoidPayment voids a payment: admins only, with a reason, once.
func handleVoidPayment(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		caller := requireIdentity(d, w, r)
		if caller == nil {
			return
		}
		if caller.Role != "Admin" {
			httpx.Error(w, http.StatusForbidden, "only an admin can void a payment", nil)
			return
		}
		var in struct {
			Reason string `json:"reason"`
			Note   string `json:"note"`
		}
		if err := httpx.Decode(r, &in); err != nil || !slices.Contains(voidReasons, in.Reason) {
			httpx.Error(w, http.StatusBadRequest, "choose a reason for voiding", err)
			return
		}
		note := strings.TrimSpace(in.Note)
		if in.Reason == "other" && len([]rune(note)) < minVoidNote {
			httpx.Error(w, http.StatusBadRequest, "explain the reason for voiding", nil)
			return
		}
		if len([]rune(note)) > 500 {
			httpx.Error(w, http.StatusBadRequest, "the reason is too long", nil)
			return
		}
		paymentID := r.PathValue("id")
		var reversed float64
		err := inTx(d, func(tx *sql.Tx) error {
			var status, enrollmentID string
			err := tx.QueryRow(`SELECT status, COALESCE(enrollment_id, '') FROM payment WHERE payment_id = ?`, paymentID).
				Scan(&status, &enrollmentID)
			if err == sql.ErrNoRows {
				return &ClientError{Status: http.StatusNotFound, Message: "no such payment"}
			}
			if err != nil {
				return err
			}
			if status == "Voided" {
				return &ClientError{Status: http.StatusConflict, Message: "this payment is already voided"}
			}
			now := time.Now().UTC().Format(time.RFC3339)
			if _, err := tx.Exec(`UPDATE payment SET status = 'Voided', voided_at = ?, voided_by = ?, void_reason = ?,
			                             stripe_checkout_url = NULL
			                       WHERE payment_id = ?`,
				now, caller.UserAccountID, voidReasonText(in.Reason, note), paymentID); err != nil {
				return err
			}
			if reversed, err = reverseVoidedCredits(tx, paymentID, in.Reason); err != nil {
				return err
			}
			ended, err := endEnrolmentOfVoided(tx, paymentID, enrollmentID)
			if err != nil {
				return err
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
				newID("pau"), paymentID, "void", caller.UserAccountID, caller.DisplayName, now, string(raw), in.Reason)
			return err
		})
		if err != nil {
			var ce *ClientError
			if errors.As(err, &ce) {
				httpx.Error(w, ce.Status, ce.Message, nil)
				return
			}
			httpx.Error(w, http.StatusInternalServerError, "could not void the payment", err)
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"payment_id": paymentID, "status": "Voided", "credits_reversed": reversed})
	}
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
		newID("ctx"), enrollmentID, studentID, classID, -net, today(), paymentID,
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
	                      WHERE enrollment_id = ? AND status = 'Active'`, today(), enrollmentID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// handlePaymentHistory is a payment's audit trail, oldest first. Staff only.
func handlePaymentHistory(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if requireStaff(d, w, r) == nil {
			return
		}
		rows, err := d.Query(`SELECT action, COALESCE(changed_by_name, ''), changed_at, COALESCE(changes, ''), COALESCE(reason, '')
		                        FROM payment_audit WHERE payment_id = ? ORDER BY changed_at, rowid`, r.PathValue("id"))
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not read the history", err)
			return
		}
		defer rows.Close()
		out := []map[string]any{}
		for rows.Next() {
			var action, by, at, changes, reason string
			if err := rows.Scan(&action, &by, &at, &changes, &reason); err != nil {
				httpx.Error(w, http.StatusInternalServerError, "could not read the history", err)
				return
			}
			entry := map[string]any{"action": action, "by": by, "at": at, "reason": reason}
			var parsed any
			if json.Unmarshal([]byte(changes), &parsed) == nil {
				entry["changes"] = parsed
			}
			out = append(out, entry)
		}
		httpx.JSON(w, http.StatusOK, out)
	}
}

func mountPaymentVoid(mux *http.ServeMux, d *sql.DB) {
	mux.HandleFunc("POST /api/v1/payments/{id}/void", handleVoidPayment(d))
	mux.HandleFunc("GET /api/v1/payments/{id}/history", handlePaymentHistory(d))
}
