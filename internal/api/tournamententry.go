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
	// The ID card check the scan returned (idcheck.go): required, and the
	// child's date of birth for the age group comes from it.
	IDCheck    string `json:"id_check"`
	CategoryID string `json:"tournament_category_id"`
	// What the public form asks too: the name called across the hall, the
	// name in Thai script (optional), and the conditions of entry.
	Nickname    string `json:"nickname"`
	NameTh      string `json:"participant_name_th"`
	AcceptTerms bool   `json:"accept_terms"`
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
		in.Nickname = strings.TrimSpace(in.Nickname)
		in.NameTh = strings.TrimSpace(in.NameTh)
		if in.Nickname == "" {
			httpx.Error(w, http.StatusBadRequest, "please give the player's nickname", nil)
			return
		}
		if len([]rune(in.Nickname)) > maxNameLen || len([]rune(in.NameTh)) > maxNameLen {
			httpx.Error(w, http.StatusBadRequest, "that name is too long", nil)
			return
		}
		// Refused rather than defaulted, as on the public form: a record of
		// agreement nobody gave is worse than none.
		if !in.AcceptTerms {
			httpx.Error(w, http.StatusBadRequest, "please accept the terms and conditions to enter", nil)
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
		/* The same doors the public form has: registration closed on the
		   tournament's card, the closing date passed, or every place taken. */
		if msg, err := entryClosed(tx, tournamentID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				httpx.Error(w, http.StatusNotFound, "no such tournament", nil)
				return
			}
			httpx.Error(w, http.StatusInternalServerError, "could not register", err)
			return
		} else if msg != "" {
			httpx.Error(w, http.StatusConflict, msg, nil)
			return
		}
		price, err := loadPrice(tx, tournamentID)
		if errors.Is(err, sql.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, "no such tournament", nil)
			return
		}
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not register", err)
			return
		}
		/* The ID card check, for this child and this tournament. Its date of
		   birth decides which category the child may enter. */
		card, msg, err := useIDCheck(tx, in.IDCheck, tournamentID, in.StudentID)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not register", err)
			return
		}
		if msg != "" {
			httpx.Error(w, http.StatusBadRequest, msg, nil)
			return
		}
		var categoryID any
		var hasCategories int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM tournament_category WHERE tournament_id = ?`,
			tournamentID).Scan(&hasCategories); err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not register", err)
			return
		}
		in.CategoryID = strings.TrimSpace(in.CategoryID)
		if in.CategoryID == "" && hasCategories > 0 {
			httpx.Error(w, http.StatusBadRequest, "please choose a category", nil)
			return
		}
		if in.CategoryID != "" {
			_, msg, err := checkCategoryAge(tx, tournamentID, in.CategoryID, card.DateOfBirth)
			if err != nil {
				httpx.Error(w, http.StatusInternalServerError, "could not register", err)
				return
			}
			if msg != "" {
				httpx.Error(w, http.StatusBadRequest, msg, nil)
				return
			}
			categoryID = in.CategoryID
		}
		var start string
		tx.QueryRow(`SELECT COALESCE(start_date,'') FROM tournament WHERE tournament_id = ?`, tournamentID).Scan(&start)

		/* The family's own contact details are already on file, so the
		   portal does not ask again: the entry carries the parent's phone and
		   email, as a public entry carries what its form gave. A phone the
		   parent typed (older portals) still wins. */
		var phone, email string
		tx.QueryRow(`SELECT COALESCE((SELECT value FROM parent_contact
		                               WHERE parent_id = ? AND contact_type = 'phone' LIMIT 1), '')`,
			id.ParentID).Scan(&phone)
		tx.QueryRow(`SELECT COALESCE((SELECT value FROM parent_contact
		                               WHERE parent_id = ? AND contact_type = 'email' LIMIT 1),
		                              (SELECT u.email FROM parent p JOIN user_account u
		                                  ON u.user_account_id = p.user_account_id WHERE p.parent_id = ?), '')`,
			id.ParentID, id.ParentID).Scan(&email)
		if in.Contact == "" {
			in.Contact = phone
		}

		// Quoted and charged are the same number: nobody reviews a family's
		// own entry, so the quote is the charge from the start.
		fee := price.StudentFee(today())

		regID := newID("treg")
		_, err = tx.Exec(`
			INSERT INTO tournament_registration (
				tournament_registration_id, tournament_id, student_id, participant_name,
				participant_contact, fee_quoted, fee_charged, student_discount_applied,
				medical_notes, remarks, early_bird_applied, priced_as_student,
				participant_date_of_birth, participant_age, tournament_category_id,
				ocr_name, ocr_date_of_birth, id_document_type,
				nickname, participant_name_th, terms_accepted_at, contact_phone, contact_email
			) VALUES (?,?,?,?,?,?,?,1,?,?,?,1,?,?,?,?,?,?,?,?,?,?,?)`,
			regID, tournamentID, in.StudentID, name, in.Contact, fee, fee,
			in.MedicalNotes, in.Remarks,
			// How it was priced, for the early-bird rule (entryrules.go).
			boolToInt(price.StudentEarlyBird && price.earlyBirdOpen(today())),
			card.DateOfBirth, nullIfZero(ageAt(card.DateOfBirth, start)), categoryID,
			nullIfEmpty(card.Name), card.DateOfBirth, nullIfEmpty(card.DocumentType),
			in.Nickname, nullIfEmpty(in.NameTh), sqliteNow(), nullIfEmpty(in.Contact), nullIfEmpty(email))
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
			"tournament_category_id":     categoryID,
			"participant_date_of_birth":  card.DateOfBirth,
		})
	}
}

func mountTournamentEntry(mux *http.ServeMux, d *sql.DB) {
	mux.HandleFunc("POST /api/v1/tournaments/{id}/entries", handleEnterTournament(d))
}

// entryClosed says why a tournament is not taking entries, or "" when it is:
// a draft, registration closed by the organiser (the public registration
// switch on the tournament's card, which closes the parent portal too), the
// closing date passed, or every place taken. The public form answers to the
// same three (registrationOpen).
func entryClosed(q interface {
	QueryRow(string, ...any) *sql.Row
}, tournamentID string) (string, error) {
	var draft, open, taken int
	var deadline string
	var capacity sql.NullInt64
	err := q.QueryRow(`
		SELECT draft, COALESCE(public_registration, 0), COALESCE(registration_deadline, ''), max_participants,
		       (SELECT COUNT(*) FROM tournament_registration r
		         WHERE r.tournament_id = t.tournament_id AND r.status IN ('Pending','Approved'))
		  FROM tournament t WHERE tournament_id = ?`, tournamentID).Scan(&draft, &open, &deadline, &capacity, &taken)
	if err != nil {
		return "", err
	}
	if draft == 1 {
		return "", sql.ErrNoRows
	}
	if open == 0 {
		return "registration for this tournament is closed", nil
	}
	var limit *int
	if capacity.Valid {
		n := int(capacity.Int64)
		limit = &n
	}
	if ok, why := registrationOpen(deadline, limit, taken); !ok {
		if why == "full" {
			return "this tournament is full", nil
		}
		return "registration for this tournament has closed", nil
	}
	return "", nil
}

