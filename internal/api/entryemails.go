// The emails a public tournament entrant gets about their entry and its fee.
//
// Registration and payment are two separate states. A place is reserved the
// moment the form is sent; the fee is Pending until it is paid, and Cancelled if
// registration closes first. Each email says which state the entry is in and
// why the family is hearing about it:
//
//   - Registration Confirmed — paid (online or at the desk), or nothing to pay;
//   - Registration Reserved — Payment Required — they chose to pay later, or
//     chose to pay now and the payment did not complete;
//   - Payment Reminder — the day before registration closes, still unpaid;
//   - Registration Cancelled — registration closed with the fee unpaid, so
//     the place was released.
//
// A family who chose "pay now" hears nothing at registration: the next thing
// they hear is either "confirmed" (paid) or "reserved, payment not completed"
// (the Stripe page closed, or they left it). Each email goes once, recorded on
// the entry, because the timer that sends most of them runs every few minutes.
package api

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"time"

	"github.com/Kusk24/jtrax-backend/internal/academytime"
	"github.com/Kusk24/jtrax-backend/internal/mail"
	"github.com/Kusk24/jtrax-backend/internal/notify"
	"github.com/Kusk24/jtrax-backend/internal/stripepay"
)

type entryNotice int

const (
	noticeConfirmed entryNotice = iota
	noticeReserved
	noticePaymentFailed
	noticeReminder
	noticeCancelled
)

// entryFacts is one entry as its emails and its pay page describe it.
type entryFacts struct {
	RegID, TournamentID, TournamentName, Participant, Category string
	Email, Source, Status                                      string
	StartDate, EndDate, VenueName, VenueAddress, Deadline      string
	// Fee is what the entry costs today: fee_charged, already moved to the
	// regular price if the early-bird date passed unpaid (entryrules.go).
	Fee              float64
	EarlyBirdApplied bool
	EarlyBirdUntil   string
	PricedAsStudent  bool
	StudentID        sql.NullString
	PaymentID        sql.NullString
	// PaymentStatus is "Pending", "Paid", "Cancelled", or "" when
	// the entry has no payment (nothing to pay, or an entry from before 0059).
	PaymentStatus string
	PaidAmount    float64
}

func loadEntryFacts(d *sql.DB, regID string) (*entryFacts, error) {
	f := entryFacts{RegID: regID}
	var paid sql.NullFloat64
	var payStatus sql.NullString
	var earlyBird, student sql.NullInt64
	err := d.QueryRow(`
		SELECT r.tournament_id, t.name, r.participant_name, COALESCE(c.name, ''),
		       r.contact_email, r.source, r.status,
		       COALESCE(t.start_date, ''), COALESCE(t.end_date, ''),
		       COALESCE(t.venue_name, ''), COALESCE(t.venue_address, ''),
		       COALESCE(t.registration_deadline, ''),
		       COALESCE(r.fee_charged, r.fee_quoted, 0), r.early_bird_applied,
		       COALESCE(t.early_bird_deadline, ''), r.priced_as_student, r.student_id,
		       p.payment_id, p.status, p.final_amount
		  FROM tournament_registration r
		  JOIN tournament t ON t.tournament_id = r.tournament_id
		  LEFT JOIN tournament_category c ON c.tournament_category_id = r.tournament_category_id
		  LEFT JOIN payment p ON p.tournament_registration_id = r.tournament_registration_id
		 WHERE r.tournament_registration_id = ?`, regID).
		Scan(&f.TournamentID, &f.TournamentName, &f.Participant, &f.Category,
			&f.Email, &f.Source, &f.Status, &f.StartDate, &f.EndDate,
			&f.VenueName, &f.VenueAddress, &f.Deadline,
			&f.Fee, &earlyBird, &f.EarlyBirdUntil, &student, &f.StudentID,
			&f.PaymentID, &payStatus, &paid)
	if err != nil {
		return nil, err
	}
	f.EarlyBirdApplied = earlyBird.Int64 == 1
	f.PricedAsStudent = student.Int64 == 1
	f.PaymentStatus = payStatus.String
	f.PaidAmount = paid.Float64
	return &f, nil
}

// earlyBirdHolds reports whether the entry still costs the early-bird price
// on `day` — it was given it, and the date has not passed.
func (f *entryFacts) earlyBirdHolds(day string) bool {
	return f.EarlyBirdApplied && f.EarlyBirdUntil != "" && day <= f.EarlyBirdUntil
}

// regularFee is what the entry will cost once its early-bird date passes,
// by the same rule lapseEarlyBird reprices it with.
func (f *entryFacts) regularFee(d *sql.DB) float64 {
	price, err := loadPrice(d, f.TournamentID)
	if err != nil {
		return f.Fee
	}
	price.EarlyBirdUntil = ""
	if f.PricedAsStudent {
		return price.StudentFee(today())
	}
	return price.OutsiderFee(today())
}

func (f *entryFacts) when() string {
	switch {
	case f.StartDate == "":
		return ""
	case f.EndDate == "" || f.EndDate == f.StartDate:
		return longDate(f.StartDate)
	default:
		return longDate(f.StartDate) + " – " + longDate(f.EndDate)
	}
}

func (f *entryFacts) venue() string {
	switch {
	case f.VenueName != "" && f.VenueAddress != "":
		return f.VenueName + ", " + f.VenueAddress
	case f.VenueName != "":
		return f.VenueName
	}
	return f.VenueAddress
}

// entryEmail writes one notice. link is the entry's pay page, "" when there
// is no portal address to build it from; online says whether the page can
// take a card, which decides how the email says to pay.
func entryEmail(d *sql.DB, f *entryFacts, kind entryNotice, link string, online bool) (string, mail.Email) {
	day := today()
	details := []mail.Detail{{Label: "Player", Value: f.Participant}, {Label: "Tournament", Value: f.TournamentName}}
	if w := f.when(); w != "" {
		details = append(details, mail.Detail{Label: "Date", Value: w})
	}
	if v := f.venue(); v != "" {
		details = append(details, mail.Detail{Label: "Venue", Value: v})
	}
	if f.Category != "" {
		details = append(details, mail.Detail{Label: "Category", Value: f.Category})
	}
	// The fee as it stands today: early-bird with its date while that holds,
	// and the price after it, so nobody is surprised by the change.
	owed := func() {
		if f.earlyBirdHolds(day) {
			details = append(details,
				mail.Detail{Label: "Early-bird fee", Value: fmtBaht(f.Fee) + " — pay by " + longDate(f.EarlyBirdUntil)},
				mail.Detail{Label: "Regular fee", Value: fmtBaht(f.regularFee(d)) + " after " + longDate(f.EarlyBirdUntil)})
		} else {
			details = append(details, mail.Detail{Label: "Entry fee", Value: fmtBaht(f.Fee)})
		}
		if f.Deadline != "" {
			details = append(details, mail.Detail{Label: "Registration closes", Value: longDate(f.Deadline)})
		}
	}
	howToPay := "Please pay by bank transfer or at the JCA front desk before registration closes."
	howToPayTH := "กรุณาโอนเงินผ่านธนาคาร หรือชำระที่เคาน์เตอร์ของ JCA ก่อนวันปิดรับสมัคร"
	var button *mail.Button
	if online && link != "" {
		howToPay = "You can pay online by card or PromptPay with the button below, or at the JCA front desk before registration closes."
		howToPayTH = "ชำระออนไลน์ด้วยบัตรหรือพร้อมเพย์ได้จากปุ่มด้านล่าง หรือชำระที่เคาน์เตอร์ของ JCA ก่อนวันปิดรับสมัคร"
		button = &mail.Button{Label: "Complete Payment", URL: link}
	}
	released := "If payment has not been received by the time registration closes, the reserved place will be released."
	releasedTH := "หากยังไม่ได้รับชำระเงินภายในวันปิดรับสมัคร สิทธิ์การแข่งขันที่จองไว้จะถูกยกเลิก"
	// The footer every email carries gives the contact details in English;
	// the Thai line stays here.
	_, note := contactLines()

	t, p := f.TournamentName, f.Participant
	switch kind {
	case noticeConfirmed:
		paras := []string{p + " has been successfully registered for " + t + "."}
		thai := p + " ลงทะเบียนเข้าร่วม " + t + " เรียบร้อยแล้ว"
		if f.PaymentStatus == "Paid" {
			details = append(details,
				mail.Detail{Label: "Payment status", Value: "Paid"},
				mail.Detail{Label: "Amount paid", Value: fmtBaht(f.PaidAmount)})
			paras = append(paras, "Your registration and payment have been completed.")
			thai += " และได้รับชำระค่าสมัครแล้ว"
		}
		if link != "" {
			button = &mail.Button{Label: "View Registration", URL: link}
		} else {
			button = nil
		}
		return "Registration Confirmed — " + p + " — " + t, mail.Email{
			Heading: "Registration confirmed", Greeting: "Hello,",
			Paragraphs: append(paras, thai), Details: details, Button: button, Note: note,
		}

	case noticeReserved, noticePaymentFailed:
		why := "You chose to pay later. Your place has been reserved, but payment has not yet been completed."
		whyTH := "คุณเลือกชำระเงินภายหลัง ที่นั่งของคุณถูกจองไว้แล้ว แต่ยังไม่ได้ชำระเงิน"
		if kind == noticePaymentFailed {
			why = "We were unable to complete your online payment. Your place has been reserved, but payment is still required."
			whyTH = "การชำระเงินออนไลน์ของคุณยังไม่สำเร็จ ที่นั่งของคุณถูกจองไว้แล้ว แต่ยังต้องชำระเงิน"
		}
		details = append(details, mail.Detail{Label: "Payment status", Value: "Pending"})
		owed()
		return "Registration Reserved — Payment Required for " + p + " — " + t, mail.Email{
			Heading: "Your place is reserved", Greeting: "Hello,",
			Paragraphs: []string{
				p + " has been successfully registered for " + t + ".",
				why, howToPay, released,
				p + " ลงทะเบียนเข้าร่วม " + t + " เรียบร้อยแล้ว " + whyTH + " " + howToPayTH + " " + releasedTH,
			},
			Details: details, Button: button, Note: note,
		}

	case noticeReminder:
		closes := longDate(f.Deadline)
		details = append(details, mail.Detail{Label: "Payment status", Value: "Pending"})
		owed()
		return "Payment Reminder — " + p + " — " + t + " registration closes " + closes, mail.Email{
			Heading: "Payment due tomorrow", Greeting: "Hello,",
			Paragraphs: []string{
				p + "'s place in " + t + " is reserved, but the entry fee has not been paid yet.",
				"Please pay by " + closes + ", when registration closes. If payment has not been received by then, the registration will be cancelled and the place released.",
				howToPay,
				"ที่นั่งของ " + p + " ใน " + t + " ยังรอการชำระเงิน กรุณาชำระภายในวันที่ " + closes +
					" ซึ่งเป็นวันปิดรับสมัคร หากยังไม่ได้รับชำระเงิน การสมัครจะถูกยกเลิกและสิทธิ์การแข่งขันจะถูกปล่อยให้ผู้อื่น " + howToPayTH,
			},
			Details: details, Button: button, Note: note,
		}

	default: // noticeCancelled
		closes := longDate(f.Deadline)
		details = append(details, mail.Detail{Label: "Payment status", Value: "Cancelled"})
		return "Registration Cancelled — " + p + " — " + t, mail.Email{
			Heading: "Registration cancelled", Greeting: "Hello,",
			Paragraphs: []string{
				"The registration for " + p + " in " + t + " has been cancelled because the entry fee was not paid by " + closes + ", when registration closed.",
				"The place has been released and is no longer reserved.",
				"If you have already paid or believe this is a mistake, please contact JCA Chess School.",
				"การสมัครของ " + p + " ใน " + t + " ถูกยกเลิกแล้ว เนื่องจากไม่ได้ชำระค่าสมัครภายในวันที่ " + closes +
					" ซึ่งเป็นวันปิดรับสมัคร สิทธิ์การแข่งขันถูกปล่อยให้ผู้อื่นแล้ว หากคุณชำระเงินแล้วหรือคิดว่าเกิดข้อผิดพลาด กรุณาติดต่อ JCA Chess School",
			},
			Details: details, Note: note,
		}
	}
}

// sendEntryNotice emails one notice to the address the entrant gave. Only
// entries from the public form: a parent's own entry is told through their
// inbox. Failures are logged, never returned — an email that did not go is
// no reason to undo a payment or a release.
func sendEntryNotice(d *sql.DB, svc *notify.Service, regID string, kind entryNotice, online bool) {
	if svc == nil {
		return
	}
	f, err := loadEntryFacts(d, regID)
	if err != nil {
		log.Printf("entry email %s: %v", regID, err)
		return
	}
	if f.Source != "Public" || f.Email == "" {
		return
	}
	var link string
	if svc.AppURL() != "" {
		code, err := currentPayCode(d, regID)
		if err != nil {
			log.Printf("entry email %s: pay code: %v", regID, err)
		} else {
			link = payLink(svc.AppURL(), f.TournamentID, regID, code)
		}
	}
	subject, e := entryEmail(d, f, kind, link, online && f.Fee > 0)
	svc.Email(f.Email, subject, e)
}

// claim stamps one of the entry's "sent" columns, and reports whether this
// call did — so two timers, or a timer and a page, never send the same email.
func claim(d *sql.DB, column, regID string) bool {
	res, err := d.Exec(`UPDATE tournament_registration SET `+column+` = ?
	                     WHERE tournament_registration_id = ? AND `+column+` IS NULL`, sqliteNow(), regID)
	if err != nil {
		log.Printf("entry email %s: %v", regID, err)
		return false
	}
	n, _ := res.RowsAffected()
	return n == 1
}

// noticeEvery is how often the timer looks for emails to send. The failed-
// payment notice waits for a Stripe page that closes after 31 minutes, so a
// few minutes more is not noticed.
const noticeEvery = 5 * time.Minute

// reminderHour is the academy hour from which a reminder may go: the day
// before closing, but not at midnight.
const reminderHour = 9

// StartEntryNotices runs RunEntryNotices on a timer until ctx ends. Started
// by the server, not by NewHandler, so tests control when it runs.
func StartEntryNotices(ctx context.Context, d *sql.DB, client *stripepay.Client, svc *notify.Service) {
	go func() {
		ticker := time.NewTicker(noticeEvery)
		defer ticker.Stop()
		for {
			RunEntryNotices(ctx, d, client, svc, academytime.Now())
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

// RunEntryNotices sends whatever is due at `now`: the failed-payment notice,
// the cancellation for places released at closing, and the day-before
// reminder. Each step logs its own errors and the others still run.
func RunEntryNotices(ctx context.Context, d *sql.DB, client *stripepay.Client, svc *notify.Service, now time.Time) {
	day := now.In(academytime.Location()).Format("2006-01-02")
	// Releases first, so a place that closed today is cancelled, not reminded.
	if err := sweepUnpaidEntries(d, day); err != nil {
		log.Printf("entry notices: %v", err)
	}
	if err := noticeFailedPayments(ctx, d, client, svc, now); err != nil {
		log.Printf("entry notices: failed payments: %v", err)
	}
	if err := noticeCancellations(ctx, d, client, svc); err != nil {
		log.Printf("entry notices: cancellations: %v", err)
	}
	if now.In(academytime.Location()).Hour() >= reminderHour {
		if err := noticeReminders(d, client, svc, day); err != nil {
			log.Printf("entry notices: reminders: %v", err)
		}
	}
}

// ids runs a query that selects entry ids and collects them, so nothing is
// written while its rows are open.
func ids(d *sql.DB, query string, args ...any) ([]string, error) {
	rows, err := d.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// noticeFailedPayments tells a "pay now" family whose payment did not go
// through that the place is held and the fee still owed — once the Stripe
// page has closed unpaid, or half an hour on if it never opened.
func noticeFailedPayments(ctx context.Context, d *sql.DB, client *stripepay.Client, svc *notify.Service, now time.Time) error {
	cutoff := now.UTC().Add(-checkoutWindow).Format(sqliteTimeLayout)
	list, err := ids(d, `
		SELECT r.tournament_registration_id FROM tournament_registration r
		  JOIN payment p ON p.tournament_registration_id = r.tournament_registration_id
		 WHERE r.source = 'Public' AND r.status = 'Approved' AND r.pay_choice = 'now'
		   AND r.reserved_emailed_at IS NULL AND p.status = 'Pending'
		   AND r.registered_at <= ?`, cutoff)
	if err != nil {
		return err
	}
	for _, regID := range list {
		var session string
		d.QueryRow(`SELECT COALESCE(stripe_session_id,'') FROM payment
		             WHERE tournament_registration_id = ?`, regID).Scan(&session)
		if session != "" && client != nil {
			// Still open means they may be paying now; paid means the
			// confirmation has just gone instead.
			outcome, err := settleFromStripe(ctx, d, client, svc, session)
			if err != nil || outcome != "expired" {
				continue
			}
		}
		if claim(d, "reserved_emailed_at", regID) {
			sendEntryNotice(d, svc, regID, noticePaymentFailed, client != nil)
		}
	}
	return nil
}

// noticeCancellations closes the Stripe page of every released place, so it
// can no longer take money, and tells the family the place has gone.
func noticeCancellations(ctx context.Context, d *sql.DB, client *stripepay.Client, svc *notify.Service) error {
	list, err := ids(d, `
		SELECT r.tournament_registration_id FROM tournament_registration r
		 WHERE r.status = 'Withdrawn' AND r.released_at IS NOT NULL
		   AND r.cancelled_emailed_at IS NULL`)
	if err != nil {
		return err
	}
	for _, regID := range list {
		var session string
		d.QueryRow(`SELECT COALESCE(stripe_session_id,'') FROM payment
		             WHERE tournament_registration_id = ? AND status = 'Cancelled'`, regID).Scan(&session)
		if session != "" {
			if client != nil {
				if err := client.ExpireCheckoutSession(ctx, session); err != nil {
					log.Printf("entry notices: closing the page for %s: %v", regID, err)
				}
			}
			d.Exec(`UPDATE payment SET stripe_session_id = NULL, stripe_checkout_url = NULL
			         WHERE tournament_registration_id = ? AND status = 'Cancelled'`, regID)
		}
		if claim(d, "cancelled_emailed_at", regID) {
			sendEntryNotice(d, svc, regID, noticeCancelled, client != nil)
		}
	}
	return nil
}

// noticeReminders sends the day-before-closing reminder to every entry still
// unpaid. Not to one made that same day or later — its first email already
// gave the date — and not to a "pay now" entry whose payment is still in
// progress (it has had no email yet; the failed-payment notice comes first).
func noticeReminders(d *sql.DB, client *stripepay.Client, svc *notify.Service, day string) error {
	tomorrow, err := time.Parse("2006-01-02", day)
	if err != nil {
		return err
	}
	closes := tomorrow.AddDate(0, 0, 1).Format("2006-01-02")
	list, err := ids(d, `
		SELECT r.tournament_registration_id FROM tournament_registration r
		  JOIN tournament t ON t.tournament_id = r.tournament_id
		 WHERE r.source = 'Public' AND r.status = 'Approved'
		   AND t.registration_deadline = ?
		   AND r.payment_reminder_sent_at IS NULL
		   AND COALESCE(r.fee_charged, r.fee_quoted, 0) > 0
		   /* registered_at is UTC; the day it fell on is Bangkok's. */
		   AND date(r.registered_at, '+7 hours') < ?
		   AND (r.pay_choice IS NULL OR r.pay_choice = 'later' OR r.reserved_emailed_at IS NOT NULL)
		   AND `+unpaid, closes, day)
	if err != nil {
		return err
	}
	for _, regID := range list {
		if claim(d, "payment_reminder_sent_at", regID) {
			sendEntryNotice(d, svc, regID, noticeReminder, client != nil)
		}
	}
	return nil
}

// noticeAfterCancelPage is the cancel page's part: a "pay now" family who
// left Stripe without paying is told straight away, rather than when the
// page closes. Once only, as everywhere.
func noticeAfterCancelPage(d *sql.DB, svc *notify.Service, regID string, online bool) {
	var choice, status string
	err := d.QueryRow(`SELECT COALESCE(pay_choice,''), status FROM tournament_registration
	                    WHERE tournament_registration_id = ?`, regID).Scan(&choice, &status)
	if errors.Is(err, sql.ErrNoRows) || err != nil || choice != "now" || status != "Approved" {
		return
	}
	var paid int
	d.QueryRow(`SELECT COUNT(*) FROM payment WHERE tournament_registration_id = ? AND status = 'Paid'`, regID).Scan(&paid)
	if paid == 0 && claim(d, "reserved_emailed_at", regID) {
		go sendEntryNotice(d, svc, regID, noticePaymentFailed, online)
	}
}
