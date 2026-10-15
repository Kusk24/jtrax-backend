// Cascading deletes for the two records the office actually removes: a student
// and a parent.
//
// The generic DELETE refuses a row anything else references, which is right for
// a class or a package — you should not silently lose a term of attendance. It
// is wrong for a person: removing a child meant hand-deleting their attendance,
// credits, payments, enrolments and the link to their parent first, in an order
// the console had to know. These endpoints do it in one transaction, in the
// order the foreign keys require, and report what went.
package api

import (
	"database/sql"
	"net/http"

	"github.com/Kusk24/jtrax-backend/internal/httpx"
)

// deleteResult is what the caller needs to tell the user afterwards: which
// people are gone, and whether a login had to be left behind.
type deleteResult struct {
	Status         string   `json:"status"`
	Student        string   `json:"student,omitempty"`
	Parent         string   `json:"parent,omitempty"`
	Children       []string `json:"children,omitempty"`
	AccountsKept   int      `json:"accounts_kept"`
	AttendanceRows int64    `json:"attendance_rows"`
	// Payments are detached, not deleted — this counts the ones kept.
	PaymentRows int64 `json:"payment_rows"`
}

// deleteAccount removes a login: its sessions, and what was only ever its own
// — the notifications it was sent, its notification switches, its phones and
// browsers. An account still referenced by history worth keeping (a game the
// student played, an announcement posted, a certificate signed) stays, but is
// retired: its email is released and it can no longer sign in, so the office
// can give the address to someone new. False when the row had to stay.
func deleteAccount(tx *sql.Tx, accountID string) bool {
	if accountID == "" {
		return true
	}
	for _, q := range []string{
		`DELETE FROM auth_session WHERE user_account_id = ?`,
		`DELETE FROM password_reset WHERE user_account_id = ?`,
		`DELETE FROM notification_delivery WHERE notification_id IN
		   (SELECT notification_id FROM notification WHERE user_account_id = ?)`,
		`DELETE FROM notification WHERE user_account_id = ?`,
		`DELETE FROM notification_setting WHERE user_account_id = ?`,
		`DELETE FROM push_subscription WHERE user_account_id = ?`,
	} {
		tx.Exec(q, accountID)
	}
	// SQLite leaves a transaction usable after a constraint failure, so a
	// refusal here does not cost the rest of the cascade.
	if _, err := tx.Exec(`DELETE FROM user_account WHERE user_account_id = ?`, accountID); err == nil {
		return true
	}
	retireAccount(tx, accountID)
	return false
}

// retireAccount frees a kept login's email and locks it: the address becomes
// one nobody can own or type (deleted+<id>@deleted.invalid) and the password
// hash one no password matches.
func retireAccount(tx *sql.Tx, accountID string) {
	tx.Exec(`UPDATE user_account
	            SET email = 'deleted+' || user_account_id || '@deleted.invalid', password_hash = '!'
	          WHERE user_account_id = ?`, accountID)
}

// removeStudent deletes one student and everything pointing at them. Order is
// forced by the foreign keys: credit_transaction references payment and
// attendance as well as the enrolment, so it goes first of all.
func removeStudent(tx *sql.Tx, studentID string, res *deleteResult) error {
	var accountID sql.NullString
	if err := tx.QueryRow(`SELECT user_account_id FROM student WHERE student_id = ?`,
		studentID).Scan(&accountID); err != nil {
		return err
	}

	if _, err := tx.Exec(`DELETE FROM credit_transaction WHERE
		enrollment_id IN (SELECT enrollment_id FROM student_enrollment WHERE student_id = ?)
		OR payment_id IN (SELECT payment_id FROM payment WHERE student_id = ?)
		OR attendance_id IN (SELECT attendance_id FROM attendance WHERE student_id = ?)`,
		studentID, studentID, studentID); err != nil {
		return err
	}

	if _, err := tx.Exec(`DELETE FROM session_booking WHERE student_id = ?`, studentID); err != nil {
		return err
	}
	att, err := tx.Exec(`DELETE FROM attendance WHERE student_id = ?`, studentID)
	if err != nil {
		return err
	}
	n, _ := att.RowsAffected()
	res.AttendanceRows += n

	// Payments are kept. The money was received, and "who paid what" has to be
	// answerable a year after the student left — so the row is detached from
	// the student rather than deleted with them, and the names it will need are
	// written onto it first. COALESCE so a payment recorded with its own
	// snapshot keeps that one: it is what was true at the till.
	pay, err := tx.Exec(`UPDATE payment SET
		student_name = COALESCE(student_name,
			(SELECT name FROM student WHERE student_id = ?)),
		class_name = COALESCE(class_name,
			(SELECT c.name FROM class c
				JOIN student_enrollment e ON e.class_id = c.class_id
				WHERE e.enrollment_id = payment.enrollment_id)),
		parent_name = COALESCE(parent_name,
			(SELECT p.name FROM parent p
				JOIN student_parent sp ON sp.parent_id = p.parent_id
				WHERE sp.student_id = ?)),
		student_id = NULL,
		enrollment_id = NULL
		WHERE student_id = ?`, studentID, studentID, studentID)
	if err != nil {
		return err
	}
	n, _ = pay.RowsAffected()
	res.PaymentRows += n

	for _, q := range []string{
		`DELETE FROM student_certificate WHERE student_id = ?`,
		`DELETE FROM student_enrollment WHERE student_id = ?`,
		`DELETE FROM practice_activity WHERE student_id = ?`,
		`DELETE FROM practice_settings WHERE student_id = ?`,
		`DELETE FROM puzzle_attempt WHERE student_id = ?`,
		`DELETE FROM puzzle_list WHERE student_id = ?`,
		/* What only ever described this child: an ID card read waiting for an
		   entry, their Lichess link and ratings, their games against the
		   engine. Each of these blocked the delete outright. */
		`DELETE FROM id_card_check WHERE student_id = ?`,
		`DELETE FROM lichess_oauth_state WHERE student_id = ?`,
		`DELETE FROM lichess_rating_day WHERE student_id = ?`,
		`DELETE FROM lichess_rating WHERE student_id = ?`,
		`DELETE FROM student_lichess WHERE student_id = ?`,
		`DELETE FROM solo_game WHERE student_id = ?`,
		/* Results the arbiter published elsewhere stay as they are; they only
		   stop pointing at a record the academy no longer has. */
		`UPDATE external_pairing SET white_student_id = NULL WHERE white_student_id = ?`,
		`UPDATE external_pairing SET black_student_id = NULL WHERE black_student_id = ?`,
		`UPDATE external_standing SET student_id = NULL WHERE student_id = ?`,
		/* An entry other rows still lean on — a game in the event's pairings,
		   or the payment kept for the accounts — cannot go without taking
		   another player's result or the money's record with it. It stays,
		   detached and with nothing left that says who played. */
		`UPDATE tournament_registration SET student_id = NULL,
		        participant_name = 'Former player', participant_name_th = NULL, nickname = '',
		        participant_contact = NULL, contact_email = '', contact_phone = '',
		        participant_date_of_birth = NULL, participant_age = NULL,
		        medical_notes = '', remarks = '', ocr_name = NULL, ocr_date_of_birth = NULL,
		        id_document_type = NULL, results_player_name = NULL
		  WHERE student_id = ? AND (
		        tournament_registration_id IN (SELECT white_registration_id FROM tournament_pairing)
		     OR tournament_registration_id IN (SELECT black_registration_id FROM tournament_pairing WHERE black_registration_id IS NOT NULL)
		     OR tournament_registration_id IN (SELECT tournament_registration_id FROM payment WHERE tournament_registration_id IS NOT NULL))`,
		`DELETE FROM tournament_registration WHERE student_id = ?`,
		`DELETE FROM student_parent WHERE student_id = ?`,
		/* The LINE chat stays; it is only no longer theirs. */
		`UPDATE line_contact SET student_id = NULL WHERE student_id = ?`,
		`DELETE FROM student WHERE student_id = ?`,
	} {
		if _, err := tx.Exec(q, studentID); err != nil {
			return err
		}
	}

	if !deleteAccount(tx, accountID.String) {
		res.AccountsKept++
	}
	return nil
}

// removeParent deletes one parent, their contacts, alert preferences and the
// links to their children. The children themselves are left alone — the caller
// decides separately whether they go too.
func removeParent(tx *sql.Tx, parentID string, res *deleteResult) error {
	var accountID sql.NullString
	if err := tx.QueryRow(`SELECT user_account_id FROM parent WHERE parent_id = ?`,
		parentID).Scan(&accountID); err != nil {
		return err
	}
	/* What they agreed to stays on record — that is the record's purpose —
	   but no longer says who they were. */
	if _, err := tx.Exec(`UPDATE consent_record SET given_by_name = ''
		WHERE user_account_id = ?`, accountID.String); err != nil {
		return err
	}
	for _, q := range []string{
		/* Deleting the guardian is how an erasure request is carried out
		   (mydata.go), so it closes theirs — before the parent row goes and
		   the request's link to it is cleared. */
		`UPDATE data_request SET status = 'Done', resolved_at = datetime('now'),
		        resolution_note = 'Erased: the guardian was deleted.'
		  WHERE parent_id = ? AND status = 'Open'`,
		`DELETE FROM parent_contact WHERE parent_id = ?`,
		`DELETE FROM notification_preference WHERE parent_id = ?`,
		`DELETE FROM student_parent WHERE parent_id = ?`,
		`UPDATE line_contact SET parent_id = NULL WHERE parent_id = ?`,
		`DELETE FROM parent WHERE parent_id = ?`,
	} {
		if _, err := tx.Exec(q, parentID); err != nil {
			return err
		}
	}
	if !deleteAccount(tx, accountID.String) {
		res.AccountsKept++
	}
	return nil
}

func childrenOf(q interface {
	Query(string, ...any) (*sql.Rows, error)
}, parentID string) ([]string, error) {
	rows, err := q.Query(`SELECT student_id FROM student_parent WHERE parent_id = ?`, parentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func mountPeopleCascade(mux *http.ServeMux, d *sql.DB) {
	// DELETE /api/v1/students/{id}/cascade[?parent=orphan]
	//
	// `parent=orphan` also removes the guardian when this was their only child —
	// a parent account with nobody attached is a login that can see nothing.
	mux.HandleFunc("DELETE /api/v1/students/{id}/cascade", func(w http.ResponseWriter, r *http.Request) {
		if requireStaff(d, w, r) == nil {
			return
		}
		studentID := r.PathValue("id")
		withParent := r.URL.Query().Get("parent") == "orphan"

		var parentID string
		d.QueryRow(`SELECT parent_id FROM student_parent WHERE student_id = ?`, studentID).Scan(&parentID)

		tx, err := d.Begin()
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not start transaction", err)
			return
		}
		defer tx.Rollback()

		res := deleteResult{Status: "deleted", Student: studentID}
		if err := removeStudent(tx, studentID, &res); err != nil {
			httpx.Error(w, http.StatusConflict, "could not delete the student", err)
			return
		}

		// Read the sibling count inside the transaction, after the link is gone:
		// asking before would have counted the child being removed.
		if withParent && parentID != "" {
			siblings, err := childrenOf(tx, parentID)
			if err != nil {
				httpx.Error(w, http.StatusInternalServerError, "could not check the guardian", err)
				return
			}
			if len(siblings) == 0 {
				if err := removeParent(tx, parentID, &res); err != nil {
					httpx.Error(w, http.StatusConflict, "could not delete the guardian", err)
					return
				}
				res.Parent = parentID
			}
		}

		if err := tx.Commit(); err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not commit", err)
			return
		}
		httpx.JSON(w, http.StatusOK, res)
	})

	// DELETE /api/v1/parents/{id}/cascade[?children=delete]
	//
	// Without the flag the children stay and simply lose their guardian, which
	// is what usually happens — one parent leaves, another is linked later.
	mux.HandleFunc("DELETE /api/v1/parents/{id}/cascade", func(w http.ResponseWriter, r *http.Request) {
		if requireStaff(d, w, r) == nil {
			return
		}
		parentID := r.PathValue("id")
		withChildren := r.URL.Query().Get("children") == "delete"

		tx, err := d.Begin()
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not start transaction", err)
			return
		}
		defer tx.Rollback()

		res := deleteResult{Status: "deleted", Parent: parentID}
		if withChildren {
			kids, err := childrenOf(tx, parentID)
			if err != nil {
				httpx.Error(w, http.StatusInternalServerError, "could not list the children", err)
				return
			}
			for _, kid := range kids {
				if err := removeStudent(tx, kid, &res); err != nil {
					httpx.Error(w, http.StatusConflict, "could not delete a child", err)
					return
				}
				res.Children = append(res.Children, kid)
			}
		}
		if err := removeParent(tx, parentID, &res); err != nil {
			httpx.Error(w, http.StatusConflict, "could not delete the parent", err)
			return
		}

		if err := tx.Commit(); err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not commit", err)
			return
		}
		httpx.JSON(w, http.StatusOK, res)
	})
}
