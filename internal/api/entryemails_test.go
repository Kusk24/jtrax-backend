package api_test

/* The emails a public entrant gets about their entry and its fee: which one,
   when, and only once. Registration and payment are two states — a place is
   reserved at once, and its fee is Pending, then Paid or Cancelled. */

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Kusk24/jtrax-backend/internal/api"
	"github.com/Kusk24/jtrax-backend/internal/mail"
	"github.com/Kusk24/jtrax-backend/internal/notify"
	"github.com/Kusk24/jtrax-backend/internal/stripepay"
)

var ict = time.FixedZone("ICT", 7*60*60)

// mails is every message sent so far, subject and body.
func (f *payFixture) mails() []struct{ To, Subject, Body string } {
	f.mail.mu.Lock()
	defer f.mail.mu.Unlock()
	return append([]struct{ To, Subject, Body string }(nil), f.mail.sent...)
}

// waitForSubject waits for a mail whose subject starts with prefix — some are
// sent off the request — and fails if none comes.
func (f *payFixture) waitForSubject(t *testing.T, prefix string) (string, string) {
	t.Helper()
	for i := 0; i < 100; i++ {
		for _, m := range f.mails() {
			if strings.HasPrefix(m.Subject, prefix) {
				return m.Subject, m.Body
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("no %q email; sent: %v", prefix, f.subjects())
	return "", ""
}

func (f *payFixture) subjects() []string {
	var out []string
	for _, m := range f.mails() {
		out = append(out, m.Subject)
	}
	return out
}

func (f *payFixture) count(prefix string) int {
	n := 0
	for _, m := range f.mails() {
		if strings.HasPrefix(m.Subject, prefix) {
			n++
		}
	}
	return n
}

// notices runs the notice timer once, at `now`, with the fixture's mail.
func (f *payFixture) notices(t *testing.T, now time.Time) {
	t.Helper()
	svc := notify.New(f.d, f.mail, mail.Config{AppURL: "https://portal.example"})
	api.RunEntryNotices(context.Background(), f.d, stripepay.New(stripepay.FromEnv()), svc, now)
	time.Sleep(50 * time.Millisecond) // the cancel-page notice is sent off the request
}

func (f *payFixture) registerChoosing(t *testing.T, email, choice string) (string, string) {
	t.Helper()
	status, out := register(t, f.pub, f.tourID, entry(map[string]any{"email": email, "payChoice": choice}), true)
	if status != 201 {
		t.Fatalf("register: %d (%v)", status, out)
	}
	return out["registrationId"].(string), out["payCode"].(string)
}

func (f *payFixture) paymentStatus(t *testing.T, regID string) string {
	t.Helper()
	var s string
	f.d.QueryRow(`SELECT status FROM payment WHERE tournament_registration_id = ?`, regID).Scan(&s)
	return s
}

// hoursFromNow is a time today or later, in Bangkok, at the given hour.
func at(dayOffset, hour int) time.Time {
	d := time.Now().In(ict).AddDate(0, 0, dayOffset)
	return time.Date(d.Year(), d.Month(), d.Day(), hour, 0, 0, 0, ict)
}

func TestPayLaterIsEmailedThatThePlaceIsReservedAndTheFeePending(t *testing.T) {
	f := publicPayServer(t, true)
	f.d.Exec(`UPDATE tournament SET start_date = '2026-12-12', venue_name = 'Assumption University',
	                 venue_address = 'Samut Prakan', registration_deadline = ? WHERE tournament_id = ?`, day(20), f.tourID)
	id, code := f.registerChoosing(t, "somchai@example.com", "later")

	subject, body := f.waitForSubject(t, "Registration Reserved — Payment Required for Somchai Niran — JCA Open")
	for _, want := range []string{
		"You chose to pay later", "Payment status: Pending", "Entry fee: 500 THB",
		"12 December 2026", "Assumption University, Samut Prakan",
		"Complete Payment", "https://portal.example/register/" + f.tourID + "/pay?entry=" + id + "#code=" + code,
		"the reserved place will be released",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("%q is missing %q:\n%s", subject, want, body)
		}
	}
	// The fee is Pending from the start, not from the first click on "Pay".
	if got := f.paymentStatus(t, id); got != "Pending" {
		t.Fatalf("payment at registration: %q, want Pending", got)
	}
}

func TestPayNowThatSucceedsGetsOneEmailRegistrationConfirmed(t *testing.T) {
	f := publicPayServer(t, true)
	id, code := f.registerChoosing(t, "somchai@example.com", "now")
	f.pub.do("POST", "/api/v1/public/tournament-registrations/"+id+"/pay", map[string]string{"code": code})
	time.Sleep(50 * time.Millisecond)
	if n := len(f.mails()); n != 0 {
		t.Fatalf("pay now should wait for the payment before emailing, sent %v", f.subjects())
	}

	var payID string
	f.d.QueryRow(`SELECT payment_id FROM payment WHERE tournament_registration_id = ?`, id).Scan(&payID)
	payload := completedEvent("cs_1", payID, 50000)
	if got := postWebhook(t, f.srv, payload, stripepay.Sign(payload, webhookSecret, time.Now())); got != 200 {
		t.Fatalf("webhook: %d", got)
	}
	_, body := f.waitForSubject(t, "Registration Confirmed — Somchai Niran — JCA Open")
	for _, want := range []string{"Payment status: Paid", "Amount paid: 500 THB", "View Registration", "#code=" + code} {
		if !strings.Contains(body, want) {
			t.Fatalf("confirmation is missing %q:\n%s", want, body)
		}
	}
	// Later passes of the timer send nothing more.
	f.notices(t, time.Now().Add(time.Hour))
	if got := f.subjects(); len(got) != 1 {
		t.Fatalf("want exactly one email, got %v", got)
	}
}

func TestPayNowThatIsAbandonedIsToldThePaymentDidNotComplete(t *testing.T) {
	f := publicPayServer(t, true)
	id, _ := f.registerChoosing(t, "somchai@example.com", "now")

	// Not yet: the Stripe page could still be open.
	f.notices(t, time.Now())
	if f.count("Registration Reserved") != 0 {
		t.Fatal("the failed-payment notice went before the page could have closed")
	}
	// Half an hour on with no payment: the place is held, the fee owed.
	f.notices(t, time.Now().Add(32*time.Minute))
	_, body := f.waitForSubject(t, "Registration Reserved — Payment Required")
	if !strings.Contains(body, "We were unable to complete your online payment") {
		t.Fatalf("should say why it was sent:\n%s", body)
	}
	f.notices(t, time.Now().Add(40*time.Minute))
	if n := f.count("Registration Reserved"); n != 1 {
		t.Fatalf("failed-payment notice sent %d times", n)
	}
	if got := f.paymentStatus(t, id); got != "Pending" {
		t.Fatalf("a failed payment leaves the fee %q, want Pending", got)
	}
}

func TestLeavingStripeSendsTheNoticeAtOnce(t *testing.T) {
	f := publicPayServer(t, true)
	id, _ := f.registerChoosing(t, "somchai@example.com", "now")
	res, err := http.Get(f.srv.URL + "/pay/cancelled?entry=" + id)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	f.waitForSubject(t, "Registration Reserved — Payment Required")

	http.Get(f.srv.URL + "/pay/cancelled?entry=" + id) // back, and away again
	f.notices(t, time.Now().Add(time.Hour))
	if n := f.count("Registration Reserved"); n != 1 {
		t.Fatalf("notice sent %d times", n)
	}
}

func TestTheDayBeforeClosingAnUnpaidEntryIsReminded(t *testing.T) {
	f := publicPayServer(t, true)
	f.d.Exec(`UPDATE tournament SET registration_deadline = ? WHERE tournament_id = ?`, day(1), f.tourID)
	unpaid, code := f.registerChoosing(t, "late@example.com", "later")
	paid, _ := f.registerChoosing(t, "paid@example.com", "later")
	today, _ := f.registerChoosing(t, "today@example.com", "later")
	// The first two entered days ago; the third only today.
	f.d.Exec(`UPDATE tournament_registration SET registered_at = ? WHERE tournament_registration_id IN (?, ?)`,
		day(-3)+" 03:00:00", unpaid, paid)
	f.d.Exec(`UPDATE payment SET status = 'Paid' WHERE tournament_registration_id = ?`, paid)
	_ = today

	f.notices(t, at(0, 7)) // too early in the day
	if f.count("Payment Reminder") != 0 {
		t.Fatal("reminder went out before 9 in the morning")
	}
	f.notices(t, at(0, 10))
	f.notices(t, at(0, 11))
	if n := f.count("Payment Reminder"); n != 1 {
		t.Fatalf("want one reminder (unpaid, entered earlier), got %d: %v", n, f.subjects())
	}
	for _, m := range f.mails() {
		if strings.HasPrefix(m.Subject, "Payment Reminder") {
			if m.To != "late@example.com" {
				t.Fatalf("reminder went to %s", m.To)
			}
			// The same link as the first email, still working.
			if !strings.Contains(m.Body, "#code="+code) || !strings.Contains(m.Body, longDay(day(1))) {
				t.Fatalf("reminder should carry the pay link and the closing date:\n%s", m.Body)
			}
		}
	}
}

func TestAPlaceUnpaidAtClosingIsCancelledItsFeeCancelledAndTheFamilyTold(t *testing.T) {
	f := publicPayServer(t, true)
	id, code := f.registerChoosing(t, "somchai@example.com", "later")
	f.pub.do("POST", "/api/v1/public/tournament-registrations/"+id+"/pay", map[string]string{"code": code})
	f.d.Exec(`UPDATE tournament_registration SET registered_at = ? WHERE tournament_registration_id = ?`, day(-5)+" 03:00:00", id)
	f.d.Exec(`UPDATE tournament SET registration_deadline = ? WHERE tournament_id = ?`, day(-1), f.tourID)

	f.notices(t, time.Now())
	f.notices(t, time.Now().Add(10*time.Minute))
	_, body := f.waitForSubject(t, "Registration Cancelled — Somchai Niran — JCA Open")
	if !strings.Contains(body, "Payment status: Cancelled") || !strings.Contains(body, "has been released") {
		t.Fatalf("cancellation should say the fee was cancelled and the place went:\n%s", body)
	}
	if n := f.count("Registration Cancelled"); n != 1 {
		t.Fatalf("cancellation sent %d times", n)
	}
	if got := f.paymentStatus(t, id); got != "Cancelled" {
		t.Fatalf("payment after closing: %q, want Cancelled", got)
	}
	var session string
	f.d.QueryRow(`SELECT COALESCE(stripe_session_id,'') FROM payment WHERE tournament_registration_id = ?`, id).Scan(&session)
	if session != "" {
		t.Fatal("the released place's Stripe page was not closed")
	}
	// The pay page says so, and will not open a payment.
	if s, e, _ := f.pub.do("POST", "/api/v1/public/tournament-registrations/"+id, map[string]string{"code": code}); s != 200 || e["state"] != "cancelled" {
		t.Fatalf("pay page after closing: %d %v", s, e)
	}
	if s, _, _ := f.pub.do("POST", "/api/v1/public/tournament-registrations/"+id+"/pay", map[string]string{"code": code}); s != 409 {
		t.Fatalf("paying a cancelled entry: %d, want 409", s)
	}
}

func TestTheReservedEmailGivesTheEarlyBirdPriceAndWhatComesAfter(t *testing.T) {
	f := publicPayServer(t, false)
	f.d.Exec(`UPDATE tournament SET regular_fee = 1000, early_bird_fee = 900, early_bird_deadline = ?,
	                 registration_deadline = ? WHERE tournament_id = ?`, day(5), day(7), f.tourID)
	f.registerChoosing(t, "somchai@example.com", "later")
	_, body := f.waitForSubject(t, "Registration Reserved")
	eb := longDay(day(5))
	for _, want := range []string{"Early-bird fee: 900 THB — pay by " + eb, "Regular fee: 1,000 THB after " + eb, "bank transfer"} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "Complete Payment") {
		t.Fatalf("no card payments, so no pay-online button:\n%s", body)
	}
}

// longDay writes a YYYY-MM-DD the way the emails do.
func longDay(iso string) string {
	d, _ := time.Parse("2006-01-02", iso)
	return d.Format("2 January 2006")
}
