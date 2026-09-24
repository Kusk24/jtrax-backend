// Paying a tournament entry fee from the parent portal.
//
// The portal has always had a payment step and it always charged nothing: it
// asked which method the family preferred, threw the answer away, and showed a
// confirmation. This is the endpoint that makes the card option real.
//
// The registration is created first, by the ordinary resource POST, and is only
// then paid for. That order is deliberate — a place in the event is what the
// parent came for, and it must survive a card that is declined, a tab that is
// closed, or a family who decide to pay at the desk after all. The fee arrives
// as an ordinary `payment` row, so a tournament fee reconciles on the payments
// screen beside everything else, and so the Stripe webhook needs no special
// case: it marks the payment Paid and grants the credits it buys, which for a
// tournament is none.
//
// The desk has its own door for the same money: a fee handed over at the
// counter is recorded against the registration it settles, so the participant
// reads Paid everywhere — the console's drawer and the parent's own portal —
// rather than Pending forever beside a payment nobody linked to it.
package api

import (
	"database/sql"
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/Kusk24/jtrax-backend/internal/httpx"
	"github.com/Kusk24/jtrax-backend/internal/stripepay"
)

// handleRegistrationStripeLink answers with a card-payment URL for one of the
// caller's own children's tournament registrations, creating the payment row
// behind it the first time.
//
// Parents only. Staff have their own door — they create the payment at the desk
// and call `payments/{id}/stripe-link` — and a Student may not commit their
// family to a fee.
func handleRegistrationStripeLink(d *sql.DB, client *stripepay.Client, cfg stripepay.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := requireIdentity(d, w, r)
		if id == nil {
			return
		}
		if id.Role != "Parent" {
			httpx.Error(w, http.StatusForbidden, "only a parent may pay for a registration", nil)
			return
		}
		if client == nil {
			httpx.Error(w, http.StatusServiceUnavailable, "card payments are not configured", nil)
			return
		}

		// The family filter is part of the lookup, not a check after it: another
		// family's registration reads as missing, which is also the only honest
		// answer to give — that it exists is not this caller's business.
		regID := r.PathValue("id")
		var studentID, participantName, status, tournamentName string
		var fee sql.NullFloat64
		var existingPayment sql.NullString
		err := d.QueryRow(`
			SELECT r.student_id, r.participant_name, r.status,
			       COALESCE(r.fee_charged, r.fee_quoted), t.name,
			       (SELECT p.payment_id FROM payment p
			         WHERE p.tournament_registration_id = r.tournament_registration_id)
			  FROM tournament_registration r
			  JOIN tournament t ON t.tournament_id = r.tournament_id
			 WHERE r.tournament_registration_id = ?
			   AND r.student_id IN (SELECT student_id FROM student_parent WHERE parent_id = ?)`,
			regID, id.ParentID).
			Scan(&studentID, &participantName, &status, &fee, &tournamentName, &existingPayment)
		if err == sql.ErrNoRows {
			httpx.Error(w, http.StatusNotFound, "no such registration", nil)
			return
		}
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not read registration", err)
			return
		}
		// A place that was turned down or given up is not one to take money for.
		if status == "Rejected" || status == "Withdrawn" {
			httpx.Error(w, http.StatusConflict, "this registration is no longer open", nil)
			return
		}
		if existingPayment.Valid && existingPayment.String != "" {
			checkoutLink(w, r, d, client, cfg, existingPayment.String)
			return
		}
		if !fee.Valid || fee.Float64 <= 0 {
			httpx.Error(w, http.StatusConflict, "this registration has no fee to pay", nil)
			return
		}

		paymentID, err := createTournamentPayment(d, regID,
			sql.NullString{String: studentID, Valid: true}, participantName, tournamentName, fee.Float64)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not open the payment", err)
			return
		}
		checkoutLink(w, r, d, client, cfg, paymentID)
	}
}

// createTournamentPayment opens the Pending payment a tournament fee is owed
// against, and returns its id.
//
// Two taps on Pay race here, and the unique index on
// `tournament_registration_id` is what settles it: the loser's INSERT fails and
// it reads back the winner's row, so both parents' tabs end up at the same
// Checkout session rather than two.
//
// studentID is NULL for a public entrant the academy has no record of: the
// payment still names them, through the snapshot columns, and simply has no
// student to point at.
func createTournamentPayment(d *sql.DB, regID string, studentID sql.NullString, participantName, tournamentName string, fee float64) (string, error) {
	// The snapshot columns exist so a payment still says who it was for after
	// the student row is gone. `class_name` holds the tournament, which is what
	// the money was for and what the payments screen needs to show.
	parentName := parentNameOf(d, studentID.String)

	paymentID := newID("pay")
	_, err := d.Exec(`
		INSERT INTO payment (payment_id, student_id, student_name, class_name, parent_name,
		                     amount, discount_amount, final_amount, payment_method, status,
		                     payment_date, tournament_registration_id)
		VALUES (?,?,?,?,?,?,0,?,'CreditCard','Pending',?,?)`,
		paymentID, studentID, participantName, tournamentName, parentName,
		fee, fee, today(), regID)
	if err == nil {
		return paymentID, nil
	}
	if !isUniqueViolation(err) {
		return "", err
	}
	var existing string
	if e := d.QueryRow(
		`SELECT payment_id FROM payment WHERE tournament_registration_id = ?`, regID).
		Scan(&existing); e != nil {
		return "", e
	}
	return existing, nil
}

// maxNoteLen bounds the two free-text boxes on the registration form. Long
// enough for a paragraph about a child's asthma, short enough that a column a
// parent can write to is not a place to park a payload — and these are the
// only free-form fields on this resource, which is otherwise names, ids and
// numbers.
const maxNoteLen = 2000

func checkRegistrationNotes(row map[string]any) error {
	for _, name := range []string{"medical_notes", "remarks"} {
		v, _ := row[name].(string)
		if len(v) > maxNoteLen {
			return errors.New(name + " is too long")
		}
	}
	return nil
}

// deskMethods are the ways the counter takes money. Card is not one of them:
// a card payment is Stripe's to confirm, through the webhook, never a member
// of staff's to assert.
var deskMethods = []string{"Cash", "PromptPay", "BankTransfer"}

// handleDeskPayment records a tournament fee paid at the front desk.
//
// Staff only — Admin and Receptionist, the people who take the money. It reuses
// the registration's payment row if the family had already opened a card link,
// so there is still one payment per registration and the unique index holds.
func handleDeskPayment(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := requireIdentity(d, w, r)
		if id == nil {
			return
		}
		if !isStaff(id.Role) {
			httpx.Error(w, http.StatusForbidden, "only staff may record a desk payment", nil)
			return
		}
		var in struct {
			Method string `json:"payment_method"`
		}
		if err := httpx.Decode(r, &in); err != nil {
			httpx.Error(w, http.StatusBadRequest, "invalid body", nil)
			return
		}
		if !slices.Contains(deskMethods, in.Method) {
			httpx.Error(w, http.StatusBadRequest,
				"payment_method must be one of "+strings.Join(deskMethods, ", "), nil)
			return
		}

		tx, err := d.Begin()
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not record the payment", err)
			return
		}
		defer tx.Rollback()

		regID := r.PathValue("id")
		var studentID, existingPayment, existingStatus sql.NullString
		var participantName, status, tournamentName string
		var fee sql.NullFloat64
		err = tx.QueryRow(`
			SELECT r.student_id, r.participant_name, r.status,
			       COALESCE(r.fee_charged, r.fee_quoted), t.name,
			       p.payment_id, p.status
			  FROM tournament_registration r
			  JOIN tournament t ON t.tournament_id = r.tournament_id
			  LEFT JOIN payment p ON p.tournament_registration_id = r.tournament_registration_id
			 WHERE r.tournament_registration_id = ?`, regID).
			Scan(&studentID, &participantName, &status, &fee, &tournamentName,
				&existingPayment, &existingStatus)
		if errors.Is(err, sql.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, "no such registration", nil)
			return
		}
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not read registration", err)
			return
		}
		if status == "Rejected" || status == "Withdrawn" {
			httpx.Error(w, http.StatusConflict, "this registration is no longer open", nil)
			return
		}
		if !fee.Valid || fee.Float64 <= 0 {
			httpx.Error(w, http.StatusConflict, "this registration has no fee to collect", nil)
			return
		}

		paymentID := existingPayment.String
		switch {
		case existingPayment.Valid && existingStatus.String != "Pending":
			// Paid is done; Refunded is a decision the payments screen made,
			// and reopening it from here would hide that it happened.
			httpx.Error(w, http.StatusConflict, "this fee is already settled", nil)
			return
		case existingPayment.Valid:
			// A card link the family opened and never finished. The link is
			// cleared so it is not handed out again for money already taken.
			_, err = tx.Exec(`
				UPDATE payment SET status = 'Paid', payment_method = ?, payment_date = ?,
				       amount = ?, final_amount = ?, stripe_checkout_url = NULL
				 WHERE payment_id = ? AND status = 'Pending'`,
				in.Method, today(), fee.Float64, fee.Float64, paymentID)
		default:
			paymentID = newID("pay")
			_, err = tx.Exec(`
				INSERT INTO payment (payment_id, student_id, student_name, class_name, parent_name,
				                     amount, discount_amount, final_amount, payment_method, status,
				                     payment_date, tournament_registration_id)
				VALUES (?,?,?,?,?,?,0,?,?,'Paid',?,?)`,
				paymentID, studentID, participantName, tournamentName,
				parentNameOf(tx, studentID.String), fee.Float64, fee.Float64, in.Method,
				today(), regID)
		}
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not record the payment", err)
			return
		}
		if err := tx.Commit(); err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not record the payment", err)
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]any{
			"payment_id": paymentID, "status": "Paid", "final_amount": fee.Float64,
		})
	}
}

func mountDeskPayment(mux *http.ServeMux, d *sql.DB) {
	mux.HandleFunc("POST /api/v1/tournament-registrations/{id}/desk-payment", handleDeskPayment(d))
}

// parentNameOf is the snapshot a payment keeps of who paid, so it still says
// after the student row is gone. Empty for somebody the academy has no record
// of — a public entrant pays at the desk too.
func parentNameOf(q interface {
	QueryRow(string, ...any) *sql.Row
}, studentID string) string {
	if studentID == "" {
		return ""
	}
	var name string
	_ = q.QueryRow(`
		SELECT p.name FROM parent p
		  JOIN student_parent sp ON sp.parent_id = p.parent_id
		 WHERE sp.student_id = ? LIMIT 1`, studentID).Scan(&name)
	return name
}

// errFeeAlreadyPaid refuses a change to the fee of an entry that has been paid.
var errFeeAlreadyPaid = errors.New("the fee is already paid; refund it on the payments screen first")

// syncRegistrationPayment keeps a registration's fee and the payment behind it
// in step, after staff edit the registration.
//
// Open card payment: repriced, and its stored Checkout link dropped so the next
// ask makes one at the new amount. A session opened at the old price is then
// refused by the webhook's amount check rather than settling the wrong sum.
// Paid: the fee is what was paid, and changing it is refused — money already
// taken is changed by a refund, not by editing a number.
func syncRegistrationPayment(tx *sql.Tx, regID string) error {
	var fee sql.NullFloat64
	var paymentID, status string
	var paid float64
	err := tx.QueryRow(`
		SELECT COALESCE(r.fee_charged, r.fee_quoted), p.payment_id, p.status, p.final_amount
		  FROM tournament_registration r
		  JOIN payment p ON p.tournament_registration_id = r.tournament_registration_id
		 WHERE r.tournament_registration_id = ?`, regID).Scan(&fee, &paymentID, &status, &paid)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if fee.Float64 == paid {
		return nil
	}
	switch status {
	case "Pending":
		_, err = tx.Exec(`
			UPDATE payment SET amount = ?, final_amount = ?,
			       stripe_session_id = NULL, stripe_checkout_url = NULL
			 WHERE payment_id = ?`, fee.Float64, fee.Float64, paymentID)
		return err
	case "Paid":
		return errFeeAlreadyPaid
	}
	return nil
}
