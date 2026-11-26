// A parent can register only their own child, at the tournament's student price.
package tournaments

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"github.com/Kusk24/jtrax-backend/internal/academytime"
	"github.com/Kusk24/jtrax-backend/internal/auth"
	"github.com/Kusk24/jtrax-backend/internal/db"
	"github.com/Kusk24/jtrax-backend/internal/families"
	"github.com/Kusk24/jtrax-backend/internal/fees"
)

type childEntryReceipt struct {
	id, playerName, dateOfBirth string
	fee                         float64
}

func registerChild(d *sql.DB, parent *auth.Identity, tournamentID string, in entryRequest) (childEntryReceipt, error) {
	var entry childEntryReceipt
	tx, err := d.Begin()
	if err != nil {
		return entry, err
	}
	defer tx.Rollback()
	// Keep the family filter in the lookup so another family's child reads as missing.
	err = tx.QueryRow(`
			SELECT COALESCE(s.name, '') FROM student s
			  JOIN student_parent sp ON sp.student_id = s.student_id
			 WHERE sp.parent_id = ? AND s.student_id = ?`, parent.ParentID, in.StudentID).Scan(&entry.playerName)
	if errors.Is(err, sql.ErrNoRows) {
		return entry, registrationRefusal{http.StatusNotFound, "no such child"}
	}
	if err != nil {
		return entry, err
	}
	price, err := parentEntryPrice(tx, tournamentID)
	if err != nil {
		return entry, err
	}
	card, err := useIDCheck(tx, in.IDCheck, tournamentID, in.StudentID)
	if err != nil {
		return entry, err
	}
	if err := checkChildEntryCategory(tx, tournamentID, in.CategoryID, card.DateOfBirth); err != nil {
		return entry, err
	}
	var start string
	tx.QueryRow(`SELECT COALESCE(start_date,'') FROM tournament WHERE tournament_id = ?`, tournamentID).Scan(&start)
	phone, email := parentEntryContact(tx, parent.ParentID)
	if in.Contact == "" {
		in.Contact = phone
	}
	entry.dateOfBirth = card.DateOfBirth
	entry.fee = price.StudentFee(academytime.Today())
	entry.id = db.NewID("treg")
	if err := insertChildEntry(tx, tournamentID, in, entry, price, card, start, email); err != nil {
		return entry, err
	}
	if in.PrivacyNotice {
		if err := families.RecordConsent(tx, "privacy_notice", entry.id, parent.UserAccountID, parent.DisplayName); err != nil {
			return entry, err
		}
	}
	if strings.TrimSpace(in.MedicalNotes) != "" {
		if err := families.RecordConsent(tx, "health_data", entry.id, parent.UserAccountID, parent.DisplayName); err != nil {
			return entry, err
		}
	}
	return entry, tx.Commit()
}

func parentEntryPrice(tx *sql.Tx, tournamentID string) (fees.TournamentPrice, error) {
	var price fees.TournamentPrice
	msg, err := entryClosed(tx, tournamentID)
	if err == nil && msg != "" {
		return price, registrationRefusal{http.StatusConflict, msg}
	}
	if err == nil {
		price, err = fees.LoadPrice(tx, tournamentID)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return price, registrationRefusal{http.StatusNotFound, "no such tournament"}
	}
	return price, err
}

func checkChildEntryCategory(tx *sql.Tx, tournamentID, categoryID, dob string) error {
	var count int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM tournament_category WHERE tournament_id = ?`, tournamentID).Scan(&count); err != nil {
		return err
	}
	if categoryID == "" && count > 0 {
		return registrationRefusal{http.StatusBadRequest, "please choose a category"}
	}
	return checkCategoryAge(tx, tournamentID, categoryID, dob)
}

func parentEntryContact(tx *sql.Tx, parentID string) (phone, email string) {
	tx.QueryRow(`SELECT COALESCE((SELECT value FROM parent_contact
		                               WHERE parent_id = ? AND contact_type = 'phone' LIMIT 1), '')`,
		parentID).Scan(&phone)
	tx.QueryRow(`SELECT COALESCE((SELECT value FROM parent_contact
		                               WHERE parent_id = ? AND contact_type = 'email' LIMIT 1),
		                              (SELECT u.email FROM parent p JOIN user_account u
		                                  ON u.user_account_id = p.user_account_id WHERE p.parent_id = ?), '')`,
		parentID, parentID).Scan(&email)
	return phone, email
}

func insertChildEntry(tx *sql.Tx, tournamentID string, in entryRequest, entry childEntryReceipt, price fees.TournamentPrice, card idCheck, start, email string) error {
	_, err := tx.Exec(`
			INSERT INTO tournament_registration (
				tournament_registration_id, tournament_id, student_id, participant_name,
				participant_contact, fee_quoted, fee_charged, student_discount_applied,
				medical_notes, remarks, early_bird_applied, priced_as_student,
				participant_date_of_birth, participant_age, tournament_category_id,
				ocr_name, ocr_date_of_birth, id_document_type,
				nickname, participant_name_th, terms_accepted_at, contact_phone, contact_email
			) VALUES (?,?,?,?,?,?,?,1,?,?,?,1,?,?,?,?,?,?,?,?,?,?,?)`,
		entry.id, tournamentID, in.StudentID, entry.playerName, in.Contact, entry.fee, entry.fee,
		in.MedicalNotes, in.Remarks,
		// How it was priced, for the early-bird rule (entryrules.go).
		boolToInt(price.StudentEarlyBird && price.EarlyBirdOpen(academytime.Today())),
		card.DateOfBirth, nullIfZero(ageAt(card.DateOfBirth, start)), db.NullIfEmpty(in.CategoryID),
		db.NullIfEmpty(card.Name), card.DateOfBirth, db.NullIfEmpty(card.DocumentType),
		in.Nickname, db.NullIfEmpty(in.NameTh), db.SQLiteNow(), db.NullIfEmpty(in.Contact), db.NullIfEmpty(email))
	if db.IsUniqueViolation(err) {
		return registrationRefusal{http.StatusConflict, "this child is already entered"}
	}
	return err
}
