// The staff side of public registration: who has entered a tournament.
//
// This was a queue, with approve and reject on the end of it. The academy takes
// every entry, so it is a roster now — the same list, read the same way, with
// nothing left to decide on it. What the desk still does to an entry it does
// through the ordinary table: correct fee_charged, or set status to Withdrawn.
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
// Everything is returned, not just the live ones: withdrawals and the rows a
// previous build rejected are part of the answer, and the desk works from this
// screen.
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

func mountRegistrationQueue(mux *http.ServeMux, d *sql.DB) {
	const p = "/api/v1/tournaments"
	mux.HandleFunc("GET "+p+"/{id}/registrations", handleRegistrationQueue(d))
}
