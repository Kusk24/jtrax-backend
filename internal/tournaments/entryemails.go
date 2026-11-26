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
package tournaments

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"time"

	"github.com/Kusk24/jtrax-backend/internal/academytime"
	"github.com/Kusk24/jtrax-backend/internal/db"
	"github.com/Kusk24/jtrax-backend/internal/entrymail"
	"github.com/Kusk24/jtrax-backend/internal/notify"
	"github.com/Kusk24/jtrax-backend/internal/payments"
	"github.com/Kusk24/jtrax-backend/internal/schedule"
	"github.com/Kusk24/jtrax-backend/internal/stripepay"
)

// claim stamps one of the entry's "sent" columns, and reports whether this
// call did — so two timers, or a timer and a page, never send the same email.
func claim(d *sql.DB, column, regID string) bool {
	res, err := d.Exec(`UPDATE tournament_registration SET `+column+` = ?
	                     WHERE tournament_registration_id = ? AND `+column+` IS NULL`, db.SQLiteNow(), regID)
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

// reminderHour is the academy hour from which a timed email may go. Every
// email a timer sends — the payment reminder, a failed payment, a released
// place, the arrival question — waits for it, so nobody is written to in the
// night; one that fell due overnight goes at 09:00. The work behind it (a
// place released, a Stripe page closed) is not held back, only the email.
const reminderHour = 9

// emailHours says whether `now` is late enough in the academy's day for a
// timed email.
func emailHours(now time.Time) bool {
	return now.In(academytime.Location()).Hour() >= reminderHour
}

// StartEntryNotices runs RunEntryNotices on a timer until ctx ends. Started
// by the server, not by NewHandler, so tests control when it runs.
func StartEntryNotices(ctx context.Context, d *sql.DB, client stripepay.CheckoutGateway, svc *notify.Service) {
	schedule.Start(ctx, noticeEvery, func() {
		RunEntryNotices(ctx, d, client, svc, academytime.Now())
	})
}

// RunEntryNotices sends whatever is due at `now`: the failed-payment notice,
// the cancellation for places released at closing, and the day-before
// reminder. Each step logs its own errors and the others still run.
func RunEntryNotices(ctx context.Context, d *sql.DB, client stripepay.CheckoutGateway, svc *notify.Service, now time.Time) {
	day := now.In(academytime.Location()).Format("2006-01-02")
	// Releases first, so a place that closed today is cancelled, not reminded.
	if err := sweepUnpaidEntries(d, day); err != nil {
		log.Printf("entry notices: %v", err)
	}
	daytime := emailHours(now)
	if err := noticeFailedPayments(ctx, d, client, svc, now, daytime); err != nil {
		log.Printf("entry notices: failed payments: %v", err)
	}
	if err := noticeCancellations(ctx, d, client, svc, daytime); err != nil {
		log.Printf("entry notices: cancellations: %v", err)
	}
	if daytime {
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
func noticeFailedPayments(ctx context.Context, d *sql.DB, client stripepay.CheckoutGateway, svc *notify.Service, now time.Time, daytime bool) error {
	cutoff := now.UTC().Add(-payments.CheckoutWindow).Format(db.SQLiteTimeLayout)
	list, err := ids(d, `
		SELECT r.tournament_registration_id FROM tournament_registration r
		  JOIN payment p ON p.tournament_registration_id = r.tournament_registration_id AND p.status <> 'Voided'
		 WHERE r.source = 'Public' AND r.status = 'Approved' AND r.pay_choice = 'now'
		   AND r.reserved_emailed_at IS NULL AND p.status = 'Pending'
		   AND r.registered_at <= ?`, cutoff)
	if err != nil {
		return err
	}
	for _, regID := range list {
		var session string
		d.QueryRow(`SELECT COALESCE(stripe_session_id,'') FROM payment
		             WHERE tournament_registration_id = ? AND status <> 'Voided'`, regID).Scan(&session)
		if session != "" && client != nil {
			// Still open means they may be paying now; paid means the
			// confirmation has just gone instead.
			outcome, err := payments.SettleFromStripe(ctx, d, client, svc, session)
			if err != nil || outcome != "expired" {
				continue
			}
		}
		// Before 09:00 the entry is left unclaimed, and the next pass after
		// it sends the email.
		if daytime && claim(d, "reserved_emailed_at", regID) {
			entrymail.SendEntryNotice(d, svc, regID, entrymail.NoticePaymentFailed, client != nil)
		}
	}
	return nil
}

// noticeCancellations closes the Stripe page of every released place, so it
// can no longer take money, and tells the family the place has gone.
func noticeCancellations(ctx context.Context, d *sql.DB, client stripepay.CheckoutGateway, svc *notify.Service, daytime bool) error {
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
		// The page is closed at once, whatever the hour; the email waits
		// for 09:00.
		if daytime && claim(d, "cancelled_emailed_at", regID) {
			entrymail.SendEntryNotice(d, svc, regID, entrymail.NoticeCancelled, client != nil)
		}
	}
	return nil
}

// noticeReminders sends the day-before-closing reminder to every entry still
// unpaid. Not to one made that same day or later — its first email already
// gave the date — and not to a "pay now" entry whose payment is still in
// progress (it has had no email yet; the failed-payment notice comes first).
func noticeReminders(d *sql.DB, client stripepay.CheckoutGateway, svc *notify.Service, day string) error {
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
			entrymail.SendEntryNotice(d, svc, regID, entrymail.NoticeReminder, client != nil)
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
		go entrymail.SendEntryNotice(d, svc, regID, entrymail.NoticePaymentFailed, online)
	}
}
