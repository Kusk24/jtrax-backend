// Paying for an entry made on the public form, by somebody with no account.
//
// A parent pays through their own sign-in (tournamentpay.go). A public entrant
// has nothing like that, so the right to pay for one entry is a secret: a
// random code handed back when they register and put in the confirmation
// email's pay link. Whoever holds the code may see that entry's fee and open a
// payment page for it. That is all it allows. It cannot change the entry, the
// fee or anything else, and the fee is always the one stored on the entry.
//
// Only the code's SHA-256 is stored, so the column is not a way in. A wrong
// code and a missing entry both answer 404, so the endpoint cannot be used to
// find out which entry ids exist.
package tournaments

import (
	"crypto/subtle"
	"database/sql"
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/Kusk24/jtrax-backend/internal/academytime"
	"github.com/Kusk24/jtrax-backend/internal/entrylinks"
	"github.com/Kusk24/jtrax-backend/internal/entrymail"
	"github.com/Kusk24/jtrax-backend/internal/httpx"
	"github.com/Kusk24/jtrax-backend/internal/mail"
	"github.com/Kusk24/jtrax-backend/internal/notify"
	"github.com/Kusk24/jtrax-backend/internal/payments"
	"github.com/Kusk24/jtrax-backend/internal/stripepay"
)

// publicEntryDeps is what the public form needs beyond the database: a way to
// email the entrant, and the card payment client, which is nil when card
// payments are switched off.
type publicEntryDeps struct {
	db        *sql.DB
	sender    mail.Sender
	mail      mail.Config
	stripe    stripepay.CheckoutGateway
	stripeCfg stripepay.Config
	// notifier sends the entry's emails (entryemails.go).
	notifier *notify.Service
}

// publicEntry is one entry as its holder may see it.
type publicEntry struct {
	TournamentID    string  `json:"tournamentId"`
	TournamentName  string  `json:"tournamentName"`
	ParticipantName string  `json:"participantName"`
	Category        string  `json:"category,omitempty"`
	Fee             float64 `json:"fee"`
	// State is what the pay page shows: "unpaid", "paid", "free" (nothing to
	// pay), "cancelled" (registration closed unpaid, so the place was released)
	// or "closed" (withdrawn, or the money was refunded).
	State        string `json:"state"`
	CardPayments bool   `json:"cardPayments"`

	StartDate string `json:"startDate,omitempty"`
	EndDate   string `json:"endDate,omitempty"`
	Venue     string `json:"venue,omitempty"`
	// Deadline is when registration closes: the last day to pay.
	Deadline string `json:"registrationDeadline,omitempty"`
	// PaymentStatus is the payment's own word: Pending, Paid or Cancelled.
	PaymentStatus string  `json:"paymentStatus"`
	AmountPaid    float64 `json:"amountPaid,omitempty"`
	// While the entry still has its early-bird price: until when, and what
	// it costs after.
	EarlyBirdUntil string  `json:"earlyBirdUntil,omitempty"`
	RegularFee     float64 `json:"regularFee,omitempty"`

	regID         string
	status        string
	studentID     sql.NullString
	paymentID     sql.NullString
	paymentStatus sql.NullString
}

var errNoEntry = errors.New("no such entry")

// findPublicEntry reads the entry the code opens. A wrong code is the same
// error as a missing entry, on purpose.
func findPublicEntry(d *sql.DB, regID, code string) (*publicEntry, error) {
	if len(code) != 64 {
		return nil, errNoEntry
	}
	e := publicEntry{regID: regID}
	var stored string
	err := d.QueryRow(`
		SELECT r.tournament_id, t.name, r.participant_name, COALESCE(c.name, ''),
		       COALESCE(r.fee_charged, r.fee_quoted, 0), r.status, r.student_id,
		       COALESCE(r.pay_code_hash, ''), p.payment_id, p.status
		  FROM tournament_registration r
		  JOIN tournament t ON t.tournament_id = r.tournament_id
		  LEFT JOIN tournament_category c ON c.tournament_category_id = r.tournament_category_id
		  LEFT JOIN payment p ON p.tournament_registration_id = r.tournament_registration_id AND p.status <> 'Voided'
		 WHERE r.tournament_registration_id = ?`, regID).
		Scan(&e.TournamentID, &e.TournamentName, &e.ParticipantName, &e.Category,
			&e.Fee, &e.status, &e.studentID, &stored, &e.paymentID, &e.paymentStatus)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNoEntry
	}
	if err != nil {
		return nil, err
	}
	// Constant time, so how long the answer takes says nothing about how much
	// of the code was right.
	if stored == "" || subtle.ConstantTimeCompare([]byte(stored), []byte(entrylinks.HashPayCode(code))) != 1 {
		return nil, errNoEntry
	}
	f, err := entrymail.LoadEntryFacts(d, regID)
	if err != nil {
		return nil, err
	}
	switch {
	case e.paymentStatus.String == "Paid":
		e.State = "paid"
	case e.paymentStatus.String == "Cancelled":
		e.State = "cancelled"
	case e.status == "Rejected" || e.status == "Withdrawn":
		e.State = "closed"
	case e.paymentStatus.Valid && e.paymentStatus.String != "Pending":
		// Refunded: a decision the payments screen made, not a debt to reopen.
		e.State = "closed"
	case e.Fee <= 0:
		e.State = "free"
	default:
		e.State = "unpaid"
	}
	e.StartDate, e.EndDate, e.Venue, e.Deadline = f.StartDate, f.EndDate, f.Venue(), f.Deadline
	e.PaymentStatus = f.PaymentStatus
	if e.PaymentStatus == "" && e.Fee > 0 {
		e.PaymentStatus = "Pending" // an entry from before its payment was made at once
	}
	if e.State == "paid" {
		e.AmountPaid = f.PaidAmount
	}
	if e.State == "unpaid" && f.EarlyBirdHolds(academytime.Today()) {
		e.EarlyBirdUntil = f.EarlyBirdUntil
		e.RegularFee = f.RegularFee(d)
	}
	return &e, nil
}

// readCode takes the code from the request body. It is never read from the
// URL, where it would be written into every log the request passes through.
func readCode(w http.ResponseWriter, r *http.Request) (string, bool) {
	var in struct {
		Code string `json:"code"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid body", nil)
		return "", false
	}
	return strings.TrimSpace(in.Code), true
}

// handlePublicEntry shows the holder of a pay link what they are paying for.
// POST rather than GET only so the code travels in a body.
func handlePublicEntry(deps *publicEntryDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		code, ok := readCode(w, r)
		if !ok {
			return
		}
		/* The early-bird and closing-date rules, applied before a price is
		   read, so a lapsed price is never the one sent to Stripe. */
		if err := sweepUnpaidEntries(deps.db, academytime.Today()); err != nil {
			log.Printf("entry rules: %v", err)
		}
		e, err := findPublicEntry(deps.db, r.PathValue("id"), code)
		if errors.Is(err, errNoEntry) {
			httpx.Error(w, http.StatusNotFound, "this payment link does not work", nil)
			return
		}
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not read the entry", err)
			return
		}
		e.CardPayments = deps.stripe != nil
		httpx.JSON(w, http.StatusOK, e)
	}
}

// handlePublicEntryPay answers with the Stripe page for the entry the code
// opens, creating the payment behind it the first time. The same payment row
// and the same link come back on every later ask, so opening the email twice
// cannot charge twice.
func handlePublicEntryPay(deps *publicEntryDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		code, ok := readCode(w, r)
		if !ok {
			return
		}
		if deps.stripe == nil {
			httpx.Error(w, http.StatusServiceUnavailable, "card payments are not switched on", nil)
			return
		}
		/* The early-bird and closing-date rules, applied before a price is
		   read, so a lapsed price is never the one sent to Stripe. */
		if err := sweepUnpaidEntries(deps.db, academytime.Today()); err != nil {
			log.Printf("entry rules: %v", err)
		}
		e, err := findPublicEntry(deps.db, r.PathValue("id"), code)
		if errors.Is(err, errNoEntry) {
			httpx.Error(w, http.StatusNotFound, "this payment link does not work", nil)
			return
		}
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not read the entry", err)
			return
		}
		switch e.State {
		case "paid":
			httpx.Error(w, http.StatusConflict, "this entry is already paid", nil)
			return
		case "free":
			httpx.Error(w, http.StatusConflict, "this entry has nothing to pay", nil)
			return
		case "closed":
			httpx.Error(w, http.StatusConflict, "this entry is no longer open for payment", nil)
			return
		case "cancelled":
			httpx.Error(w, http.StatusConflict, "registration has closed and this place was released", nil)
			return
		}
		paymentID := e.paymentID.String
		if !e.paymentID.Valid {
			paymentID, err = createTournamentPayment(deps.db, e.regID, e.studentID,
				e.ParticipantName, e.TournamentName, e.Fee)
			if err != nil {
				httpx.Error(w, http.StatusInternalServerError, "could not open the payment", err)
				return
			}
		}
		payments.CheckoutLink(w, r, deps.db, deps.stripe, deps.stripeCfg, paymentID)
	}
}
