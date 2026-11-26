// Public entries reserve a place and record consent atomically. Payment and email follow the commit.
package tournaments

import (
	"database/sql"
	"errors"
	"log"
	"net/http"

	"github.com/Kusk24/jtrax-backend/internal/db"
	"github.com/Kusk24/jtrax-backend/internal/entrylinks"
	"github.com/Kusk24/jtrax-backend/internal/entrymail"
	"github.com/Kusk24/jtrax-backend/internal/families"
)

type publicEntryReceipt struct {
	id, payCode, tournamentName, payChoice string
	studentID                              sql.NullString
	fee                                    float64
	earlyBird                              bool
}

func registerPublicEntry(d *sql.DB, tournamentID string, in registerInput, online bool) (publicEntryReceipt, error) {
	entry := publicEntryReceipt{id: db.NewID("treg")}
	// Creating the pay-code key can itself write to the database on first use.
	code, hash, err := entrylinks.EntryPayCode(d, entry.id)
	if err != nil {
		return entry, err
	}
	entry.payCode = code
	tx, err := d.Begin()
	if err != nil {
		return entry, err
	}
	defer tx.Rollback()

	t, err := openPublicTournament(tx, tournamentID)
	if err != nil {
		return entry, err
	}
	card, err := useIDCheck(tx, in.IDCheck, tournamentID, "")
	if err != nil {
		return entry, err
	}
	in.DateOfBirth, in.ScannedDateOfBirth, in.ScannedName = card.DateOfBirth, card.DateOfBirth, card.Name
	if card.DocumentType != "" {
		in.DocumentType = card.DocumentType
	}
	in.Age = ageAt(card.DateOfBirth, t.StartDate)
	if err := checkCategoryAge(tx, tournamentID, in.CategoryID, card.DateOfBirth); err != nil {
		return entry, err
	}
	entry.studentID, err = publicEntrantStudent(tx, in)
	if err != nil {
		return entry, err
	}

	entry.tournamentName = t.Name
	entry.fee, entry.earlyBird = t.Fee, t.EarlyBirdActive
	if in.IsStudent {
		entry.fee, entry.earlyBird = t.StudentFee, t.price.StudentEarlyBird && t.EarlyBirdActive
	}
	entry.payChoice = in.PayChoice
	switch {
	case entry.fee <= 0:
		entry.payChoice = ""
	case !online || entry.payChoice == "":
		entry.payChoice = "later"
	}
	if err := insertPublicEntry(tx, tournamentID, in, entry, hash); err != nil {
		return entry, err
	}
	if err := families.RecordConsent(tx, "privacy_notice", entry.id, "", "public form"); err != nil {
		return entry, err
	}
	return entry, tx.Commit()
}

func openPublicTournament(tx *sql.Tx, tournamentID string) (*publicTournament, error) {
	row := tx.QueryRow(publicTournamentSelect+`
			WHERE t.tournament_id = ? AND t.public_registration = 1 AND t.draft = 0`, tournamentID)
	t, err := scanPublicTournament(row)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil, registrationRefusal{http.StatusNotFound, "not found"}
	case err != nil:
		return nil, err
	case t.Open:
		return t, nil
	case t.ClosedReason == "full":
		return nil, registrationRefusal{http.StatusConflict, "this tournament is full"}
	default:
		return nil, registrationRefusal{http.StatusConflict, "registration for this tournament has closed"}
	}
}

// A declared student must exist. A quiet email match links the roster without changing the fee.
func publicEntrantStudent(tx *sql.Tx, in registerInput) (sql.NullString, error) {
	if in.IsStudent {
		var count int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM student WHERE student_id = ?`, in.StudentID).Scan(&count); err != nil {
			return sql.NullString{}, err
		}
		if count == 0 {
			return sql.NullString{}, registrationRefusal{http.StatusBadRequest, "we could not find that JCA student ID — check it, or untick the student box"}
		}
		return sql.NullString{String: in.StudentID, Valid: true}, nil
	}
	var matched string
	err := tx.QueryRow(`SELECT s.student_id FROM student s
			                   JOIN user_account u ON u.user_account_id = s.user_account_id
			                   WHERE lower(trim(u.email)) = ?`, in.Email).Scan(&matched)
	if errors.Is(err, sql.ErrNoRows) {
		return sql.NullString{}, nil
	}
	return sql.NullString{String: matched, Valid: err == nil}, err
}

func insertPublicEntry(tx *sql.Tx, tournamentID string, in registerInput, entry publicEntryReceipt, payCodeHash string) error {
	_, err := tx.Exec(`INSERT INTO tournament_registration (
			tournament_registration_id, tournament_id, student_id, participant_name,
			participant_date_of_birth, tournament_category_id, registered_at,
			status, source, contact_email, contact_phone, fee_quoted, fee_charged,
			student_discount_applied, nickname, participant_age, terms_accepted_at,
			pay_code_hash, early_bird_applied, priced_as_student,
			participant_name_th, id_document_type, ocr_name, ocr_date_of_birth, pay_choice
		) VALUES (?,?,?,?,?,?,?,'Approved','Public',?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		entry.id, tournamentID, entry.studentID, in.Name,
		db.NullIfEmpty(in.DateOfBirth), db.NullIfEmpty(in.CategoryID), db.SQLiteNow(),
		in.Email, in.Phone, entry.fee, entry.fee, boolToInt(in.IsStudent),
		in.Nickname, nullIfZero(in.Age),
		/* Stamped here rather than taken from the request: the time the
		   terms were accepted is the server's fact, and a client-supplied
		   timestamp on a consent record is worth nothing. validate()
		   refuses the entry unless AcceptTerms is true, so reaching this
		   line is what "accepted" means. */
		db.SQLiteNow(), payCodeHash,
		/* How it was priced, for the early-bird rule (entryrules.go). */
		boolToInt(entry.earlyBird), boolToInt(in.IsStudent),
		db.NullIfEmpty(in.NameTh), db.NullIfEmpty(in.DocumentType),
		db.NullIfEmpty(in.ScannedName), db.NullIfEmpty(in.ScannedDateOfBirth), db.NullIfEmpty(entry.payChoice))
	if db.IsUniqueViolation(err) {
		return registrationRefusal{http.StatusConflict, "this player is already registered for this tournament"}
	}
	return err
}

func (deps *publicEntryDeps) openPaymentAndNotify(entry publicEntryReceipt, playerName string) {
	// The fee is Pending from the moment the place is taken, whichever way
	// it will be paid, so a fee nobody pays has something to expire.
	if entry.fee > 0 {
		sid := entry.studentID.String
		if _, err := createTournamentPayment(deps.db, entry.id, sql.NullString{String: sid, Valid: sid != ""},
			playerName, entry.tournamentName, entry.fee); err != nil {
			// The pay page makes it on first use; nothing is lost.
			log.Printf("public entry %s: opening its payment: %v", entry.id, err)
		}
	}

	// Off the request, so a slow mail server does not hold the entrant at a
	// spinner after their place is already theirs. "now" waits: the next
	// email is "confirmed" or "payment not completed" (entryemails.go).
	online := deps.stripe != nil
	switch {
	case entry.fee <= 0:
		go entrymail.SendEntryNotice(deps.db, deps.notifier, entry.id, entrymail.NoticeConfirmed, online)
	case entry.payChoice == "later" && claim(deps.db, "reserved_emailed_at", entry.id):
		go entrymail.SendEntryNotice(deps.db, deps.notifier, entry.id, entrymail.NoticeReserved, online)
	}
}
