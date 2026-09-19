// A parent entering their own child in a tournament.
//
// This used to be the generic registration POST, which wrote whatever the
// parent's request carried — including `fee_charged` and `status`. The card
// payment charges `fee_charged`, so a family could enter at a price they chose
// and pay exactly that. Here the parent says only what is theirs to say — which
// child, a contact number, and the two notes — and the server fills in the
// rest: the child's name from the academy's records, the price from the
// tournament (pricing.go), and the status staff entry has always had.
package api

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"github.com/Kusk24/jtrax-backend/internal/httpx"
)

type entryRequest struct {
	StudentID    string `json:"student_id"`
	Contact      string `json:"participant_contact"`
	MedicalNotes string `json:"medical_notes"`
	Remarks      string `json:"remarks"`
}

// handleEnterTournament registers one of the caller's children for the
// tournament in the path, priced by the server.
//
// Parents only. Staff register at the desk through the ordinary resource, where
// setting a fee by hand is their job; a Student may not commit their family to
// a fee.
func handleEnterTournament(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := requireIdentity(d, w, r)
		if id == nil {
			return
		}
		if id.Role != "Parent" {
			httpx.Error(w, http.StatusForbidden, "only a parent may enter their child", nil)
			return
		}
		// Unknown fields are refused rather than ignored, so a request that
		// still carries a fee fails loudly instead of appearing to work.
		var in entryRequest
		if err := httpx.Decode(r, &in); err != nil {
			httpx.Error(w, http.StatusBadRequest, "invalid body", nil)
			return
		}
		in.StudentID = strings.TrimSpace(in.StudentID)
		in.Contact = strings.TrimSpace(in.Contact)
		if in.StudentID == "" {
			httpx.Error(w, http.StatusBadRequest, "student_id is required", nil)
			return
		}
		if len(in.Contact) > maxPhoneLen {
			httpx.Error(w, http.StatusBadRequest, "participant_contact is too long", nil)
			return
		}
		if err := checkRegistrationNotes(map[string]any{
			"medical_notes": in.MedicalNotes, "remarks": in.Remarks,
		}); err != nil {
			httpx.Error(w, http.StatusBadRequest, err.Error(), nil)
			return
		}

		tx, err := d.Begin()
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not register", err)
			return
		}
		defer tx.Rollback()

		// The family filter is part of the lookup: somebody else's child reads
		// as missing, which is also all this caller needs to know.
		var name string
		err = tx.QueryRow(`
			SELECT COALESCE(s.name, '') FROM student s
			  JOIN student_parent sp ON sp.student_id = s.student_id
			 WHERE sp.parent_id = ? AND s.student_id = ?`,
			id.ParentID, in.StudentID).Scan(&name)
		if errors.Is(err, sql.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, "no such child", nil)
			return
		}
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not register", err)
			return
		}

		tournamentID := r.PathValue("id")
		price, err := loadPrice(tx, tournamentID)
		if errors.Is(err, sql.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, "no such tournament", nil)
			return
		}
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not register", err)
			return
		}
		// Quoted and charged are the same number: nobody reviews a family's
		// own entry, so the quote is the charge from the start.
		fee := price.StudentFee(todayISO())

		regID := newID("treg")
		_, err = tx.Exec(`
			INSERT INTO tournament_registration (
				tournament_registration_id, tournament_id, student_id, participant_name,
				participant_contact, fee_quoted, fee_charged, student_discount_applied,
				medical_notes, remarks
			) VALUES (?,?,?,?,?,?,?,1,?,?)`,
			regID, tournamentID, in.StudentID, name, in.Contact, fee, fee,
			in.MedicalNotes, in.Remarks)
		if isUniqueViolation(err) {
			httpx.Error(w, http.StatusConflict, "this child is already entered", nil)
			return
		}
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not register", err)
			return
		}
		if err := tx.Commit(); err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not register", err)
			return
		}
		httpx.JSON(w, http.StatusCreated, map[string]any{
			"tournament_registration_id": regID,
			"tournament_id":              tournamentID,
			"student_id":                 in.StudentID,
			"participant_name":           name,
			"status":                     "Approved",
			"fee_charged":                fee,
		})
	}
}

func mountTournamentEntry(mux *http.ServeMux, d *sql.DB) {
	mux.HandleFunc("POST /api/v1/tournaments/{id}/entries", handleEnterTournament(d))
}
