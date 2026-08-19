// The staff side of public registration: the queue of people waiting to be let
// into a tournament, and the two decisions that empty it.
//
// This exists as its own endpoint rather than as a registry read because the
// desk needs one thing the table cannot give it — whether the address somebody
// registered with belongs to a student the academy already knows. That match is
// deliberately withheld from the public reply (see publicregistration.go) and
// surfaced only here, which is the whole reason for the split.
package api

import (
	"database/sql"
	"net/http"

	"github.com/Kusk24/jtrax-backend/internal/httpx"
)

type queueEntry struct {
	ID           string   `json:"id"`
	Name         string   `json:"participantName"`
	Email        string   `json:"contactEmail"`
	Phone        string   `json:"contactPhone"`
	DateOfBirth  string   `json:"dateOfBirth,omitempty"`
	Category     string   `json:"category,omitempty"`
	Status       string   `json:"status"`
	Source       string   `json:"source"`
	RegisteredAt string   `json:"registeredAt"`
	FeeQuoted    *float64 `json:"feeQuoted"`
	FeeCharged   *float64 `json:"feeCharged"`
	// What the registrant said about themselves.
	ClaimedStudent bool `json:"claimedStudent"`
	// What the academy's own records say. The two disagreeing is the single
	// most useful thing on this screen: a claimed discount with no matching
	// student is exactly what a member of staff is here to judge.
	MatchedStudentID   string `json:"matchedStudentId,omitempty"`
	MatchedStudentName string `json:"matchedStudentName,omitempty"`
}

// handleRegistrationQueue lists a tournament's registrations for staff.
//
// Everything is returned, not just the pending ones: the desk works from this
// screen, and "who did we turn away" is part of the answer.
func handleRegistrationQueue(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if requireStaff(d, w, r) == nil {
			return
		}
		tournamentID := r.PathValue("id")
		rows, err := d.Query(`
			SELECT reg.tournament_registration_id, reg.participant_name,
			       reg.contact_email, reg.contact_phone,
			       COALESCE(reg.participant_date_of_birth,''),
			       COALESCE(cat.name,''), reg.status, reg.source, reg.registered_at,
			       reg.fee_quoted, reg.fee_charged, reg.student_discount_applied,
			       COALESCE(reg.student_id,''), COALESCE(stu.name,'')
			FROM tournament_registration reg
			LEFT JOIN tournament_category cat
			       ON cat.tournament_category_id = reg.tournament_category_id
			LEFT JOIN student stu ON stu.student_id = reg.student_id
			WHERE reg.tournament_id = ?
			-- Pending first: this screen exists to be emptied.
			ORDER BY CASE reg.status WHEN 'Pending' THEN 0 ELSE 1 END,
			         reg.registered_at DESC`, tournamentID)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not load registrations", err)
			return
		}
		defer rows.Close()

		out := []queueEntry{}
		for rows.Next() {
			var e queueEntry
			var claimed int
			if err := rows.Scan(&e.ID, &e.Name, &e.Email, &e.Phone, &e.DateOfBirth,
				&e.Category, &e.Status, &e.Source, &e.RegisteredAt,
				&e.FeeQuoted, &e.FeeCharged, &claimed,
				&e.MatchedStudentID, &e.MatchedStudentName); err != nil {
				httpx.Error(w, http.StatusInternalServerError, "could not load registrations", err)
				return
			}
			e.ClaimedStudent = claimed == 1
			out = append(out, e)
		}
		if err := rows.Err(); err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not load registrations", err)
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"registrations": out})
	}
}

// decisionInput is what staff send when they act on an entry. The fee is
// optional and overrides the quote: the desk is the authority on whether a
// claimed student discount really applies, and correcting it here is the point.
type decisionInput struct {
	Fee *float64 `json:"fee"`
}

// handleRegistrationDecision approves or rejects one entry.
//
// Only a Pending row can be decided. Deciding an already-decided one is a
// conflict rather than a silent overwrite — two people working the same queue
// must not be able to quietly undo each other.
func handleRegistrationDecision(d *sql.DB, decision string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := requireStaff(d, w, r)
		if id == nil {
			return
		}
		regID := r.PathValue("regId")
		var in decisionInput
		if r.ContentLength > 0 {
			if err := httpx.Decode(r, &in); err != nil {
				httpx.Error(w, http.StatusBadRequest, "invalid body", err)
				return
			}
		}
		if in.Fee != nil && (*in.Fee < 0 || *in.Fee > 1_000_000) {
			httpx.Error(w, http.StatusBadRequest, "that fee is out of range", nil)
			return
		}

		// Approving is also when the fee stops being a quote and becomes a
		// charge, so fee_charged is set from the override or the quote.
		var res sql.Result
		var err error
		if decision == "Approved" {
			res, err = d.Exec(`UPDATE tournament_registration
				SET status = 'Approved', reviewed_at = ?, reviewed_by = ?,
				    fee_charged = COALESCE(?, fee_quoted),
				    fee_quoted  = COALESCE(?, fee_quoted)
				WHERE tournament_registration_id = ? AND status = 'Pending'`,
				sqliteNow(), id.UserAccountID, in.Fee, in.Fee, regID)
		} else {
			res, err = d.Exec(`UPDATE tournament_registration
				SET status = 'Rejected', reviewed_at = ?, reviewed_by = ?
				WHERE tournament_registration_id = ? AND status = 'Pending'`,
				sqliteNow(), id.UserAccountID, regID)
		}
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not update the registration", err)
			return
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			// Either it does not exist or somebody has already decided it. The
			// desk needs the same nudge for both: reload and look again.
			httpx.Error(w, http.StatusConflict,
				"that registration has already been decided, or no longer exists", nil)
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"status": decision})
	}
}

// handleApproveCapacityCheck wraps approval with the one rule the public path
// enforces and this one otherwise would not: a tournament cannot be approved
// past its own capacity. Staff may still add entries directly, but they should
// not be able to walk past the limit by clicking Approve without noticing.
func handleApproveRegistration(d *sql.DB) http.HandlerFunc {
	approve := handleRegistrationDecision(d, "Approved")
	return func(w http.ResponseWriter, r *http.Request) {
		if requireStaff(d, w, r) == nil {
			return
		}
		var capacity sql.NullInt64
		var approved int
		err := d.QueryRow(`
			SELECT t.max_participants,
			       (SELECT COUNT(*) FROM tournament_registration r2
			         WHERE r2.tournament_id = t.tournament_id AND r2.status = 'Approved')
			FROM tournament t
			JOIN tournament_registration r ON r.tournament_id = t.tournament_id
			WHERE r.tournament_registration_id = ?`, r.PathValue("regId")).
			Scan(&capacity, &approved)
		if err == nil && capacity.Valid && approved >= int(capacity.Int64) {
			httpx.Error(w, http.StatusConflict,
				"this tournament is already full — raise the limit to approve more", nil)
			return
		}
		approve(w, r)
	}
}

func mountRegistrationQueue(mux *http.ServeMux, d *sql.DB) {
	const p = "/api/v1/tournaments"
	mux.HandleFunc("GET "+p+"/{id}/registrations", handleRegistrationQueue(d))
	mux.HandleFunc("POST "+p+"/registrations/{regId}/approve", handleApproveRegistration(d))
	mux.HandleFunc("POST "+p+"/registrations/{regId}/reject", handleRegistrationDecision(d, "Rejected"))
}
