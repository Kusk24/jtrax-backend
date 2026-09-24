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
package api

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"log"
	"net/http"
	"net/url"
	"strings"

	"github.com/Kusk24/jtrax-backend/internal/httpx"
	"github.com/Kusk24/jtrax-backend/internal/mail"
	"github.com/Kusk24/jtrax-backend/internal/stripepay"
)

// publicEntryDeps is what the public form needs beyond the database: a way to
// email the entrant, and the card payment client, which is nil when card
// payments are switched off.
type publicEntryDeps struct {
	db        *sql.DB
	sender    mail.Sender
	mail      mail.Config
	stripe    *stripepay.Client
	stripeCfg stripepay.Config
}

// newPayCode returns a fresh code and the hash to store for it. 32 random
// bytes: nobody guesses that, through a rate limit or otherwise.
func newPayCode() (code, hash string, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", err
	}
	code = hex.EncodeToString(raw)
	return code, hashPayCode(code), nil
}

func hashPayCode(code string) string {
	sum := sha256.Sum256([]byte(code))
	return hex.EncodeToString(sum[:])
}

// payLink is the page the confirmation email points at. The code goes after
// the #, which a browser never sends to a server, so it stays out of the web
// host's request logs. The page reads it from there and posts it in a body.
func payLink(appURL, tournamentID, regID, code string) string {
	return strings.TrimSuffix(appURL, "/") + "/register/" + url.PathEscape(tournamentID) +
		"/pay?entry=" + url.QueryEscape(regID) + "#code=" + code
}

// publicEntry is one entry as its holder may see it.
type publicEntry struct {
	TournamentID    string  `json:"tournamentId"`
	TournamentName  string  `json:"tournamentName"`
	ParticipantName string  `json:"participantName"`
	Category        string  `json:"category,omitempty"`
	Fee             float64 `json:"fee"`
	// State is what the pay page shows: "unpaid", "paid", "free" (nothing to
	// pay) or "closed" (withdrawn, or the money was refunded).
	State        string `json:"state"`
	CardPayments bool   `json:"cardPayments"`

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
		  LEFT JOIN payment p ON p.tournament_registration_id = r.tournament_registration_id
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
	if stored == "" || subtle.ConstantTimeCompare([]byte(stored), []byte(hashPayCode(code))) != 1 {
		return nil, errNoEntry
	}
	switch {
	case e.status == "Rejected" || e.status == "Withdrawn":
		e.State = "closed"
	case e.paymentStatus.String == "Paid":
		e.State = "paid"
	case e.paymentStatus.Valid && e.paymentStatus.String != "Pending":
		// Refunded: a decision the payments screen made, not a debt to reopen.
		e.State = "closed"
	case e.Fee <= 0:
		e.State = "free"
	default:
		e.State = "unpaid"
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
		checkoutLink(w, r, deps.db, deps.stripe, deps.stripeCfg, paymentID)
	}
}

// sendEntryConfirmation emails the entrant that they are in, with the fee and,
// when card payments are on, the link to pay it. It runs after the entry is
// committed and never fails the registration: an email that did not send is
// no reason to tell somebody their place was not taken.
func sendEntryConfirmation(deps *publicEntryDeps, to, tournamentID, tournamentName, regID, participant, category string, fee float64, code string) {
	var link string
	if deps.stripe != nil && fee > 0 && deps.mail.AppURL != "" {
		link = payLink(deps.mail.AppURL, tournamentID, regID, code)
	}
	if deps.sender == nil {
		// No SMTP configured, which only happens in development. The link is a
		// credential for this entry's payment, so the log line says so.
		if link != "" {
			log.Printf("public entry: SMTP not configured — pay link for %s (SENSITIVE): %s", regID, link)
		}
		return
	}
	subject := "You're registered: " + tournamentName
	if err := deps.sender.Send(to, subject, entryConfirmationBody(tournamentName, participant, category, fee, link)); err != nil {
		// The address is the entrant's, which is personal data; the entry id
		// is enough for somebody to find it.
		log.Printf("public entry: confirmation for %s did not send: %v", regID, err)
	}
}

// entryConfirmationBody is the email, English then Thai, plain text like every
// other message the academy sends.
func entryConfirmationBody(tournament, participant, category string, fee float64, link string) string {
	var b strings.Builder
	b.WriteString("Hello,\n\n")
	b.WriteString(participant + " is registered for " + tournament + ".\n")
	if category != "" {
		b.WriteString("Category: " + category + "\n")
	}
	if fee > 0 {
		b.WriteString("Entry fee: " + fmtBaht(fee) + "\n\n")
		if link != "" {
			b.WriteString("Pay by card or PromptPay here:\n" + link + "\n\n")
			b.WriteString("Or pay by bank transfer or at the JCA front desk.\n")
			b.WriteString("Keep this email: the link is how you pay for this entry, and it works only for this entry.\n")
		} else {
			b.WriteString("Please pay by bank transfer or at the JCA front desk.\n")
		}
	}
	b.WriteString("\nJCA Chess Academy\n\n----------\n\n")

	b.WriteString("สวัสดีค่ะ\n\n")
	b.WriteString(participant + " ลงทะเบียนเข้าร่วม " + tournament + " เรียบร้อยแล้ว\n")
	if category != "" {
		b.WriteString("รุ่น: " + category + "\n")
	}
	if fee > 0 {
		b.WriteString("ค่าสมัคร: " + fmtBaht(fee) + "\n\n")
		if link != "" {
			b.WriteString("ชำระด้วยบัตรหรือพร้อมเพย์ได้ที่:\n" + link + "\n\n")
			b.WriteString("หรือโอนเงินผ่านธนาคาร หรือชำระที่เคาน์เตอร์ของ JCA\n")
			b.WriteString("กรุณาเก็บอีเมลนี้ไว้ ลิงก์นี้ใช้ชำระเงินสำหรับการสมัครนี้เท่านั้น\n")
		} else {
			b.WriteString("กรุณาโอนเงินผ่านธนาคาร หรือชำระที่เคาน์เตอร์ของ JCA\n")
		}
	}
	b.WriteString("\nJCA Chess Academy\n")
	return b.String()
}
