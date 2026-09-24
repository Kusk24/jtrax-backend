package api_test

/* A paid tournament fee is confirmed to whoever owes it, however it was paid:
   a parent through their inbox, a public entrant by email to the address they
   registered with. */

import (
	"strings"
	"testing"
	"time"

	"github.com/Kusk24/jtrax-backend/internal/stripepay"
)

// A fee taken at the counter used to settle silently: the desk writes SQL
// directly, so the payments resource's receipt hook never ran.
func TestAFeePaidAtTheDeskIsConfirmedToTheParent(t *testing.T) {
	srv := newServer(t)
	parent := &client{t: t, srv: srv}
	parent.login("sandy01234@gmail.com")
	regID := registerPenny(t, parent, 800)

	desk := &client{t: t, srv: srv}
	desk.login("admin@jca.ac.th")
	if status, _, _ := desk.do("POST", "/api/v1/tournament-registrations/"+regID+"/desk-payment",
		map[string]any{"payment_method": "Cash"}); status != 200 {
		t.Fatalf("desk payment: %d", status)
	}

	in := inbox(t, srv, "sandy01234@gmail.com")
	if got := countType(in, "payment_received"); got != 1 {
		t.Fatalf("the parent should get one receipt, got %d", got)
	}
	if body := bodyOfType(in, "payment_received"); !strings.Contains(body, "800 THB") || !strings.Contains(body, "Penny") {
		t.Fatalf("the receipt should say how much and for whom: %q", body)
	}
}

// waitForReceipt waits for a mail whose body says the payment arrived. The
// confirmation of the entry itself is also mail, so "any mail" is not enough.
func waitForReceipt(t *testing.T, f *payFixture) (string, string) {
	t.Helper()
	for i := 0; i < 100; i++ {
		f.mail.mu.Lock()
		for _, m := range f.mail.sent {
			if strings.Contains(m.Body, "We have received your payment") {
				f.mail.mu.Unlock()
				return m.To, m.Body
			}
		}
		f.mail.mu.Unlock()
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("no payment receipt was emailed")
	return "", ""
}

func TestAPublicEntrantIsEmailedTheirReceiptAfterPayingByCard(t *testing.T) {
	f := publicPayServer(t, true)
	id, code, _ := f.register(t, "somchai@example.com")
	f.pub.do("POST", "/api/v1/public/tournament-registrations/"+id+"/pay", map[string]string{"code": code})

	var payID string
	f.d.QueryRow(`SELECT payment_id FROM payment WHERE tournament_registration_id = ?`, id).Scan(&payID)
	payload := completedEvent("cs_1", payID, 50000)
	if got := postWebhook(t, f.srv, payload, stripepay.Sign(payload, webhookSecret, time.Now())); got != 200 {
		t.Fatalf("webhook: %d", got)
	}

	to, body := waitForReceipt(t, f)
	if to != "somchai@example.com" || !strings.Contains(body, "500 THB") || !strings.Contains(body, "JCA Open") {
		t.Fatalf("receipt to %q:\n%s", to, body)
	}
}

func TestAPublicEntrantIsEmailedTheirReceiptWhenTheDeskTakesTheFee(t *testing.T) {
	f := publicPayServer(t, false)
	id, _, _ := f.register(t, "somchai@example.com")

	desk := &client{t: t, srv: f.srv}
	desk.login("admin@jca.ac.th")
	if status, _, _ := desk.do("POST", "/api/v1/tournament-registrations/"+id+"/desk-payment",
		map[string]any{"payment_method": "BankTransfer"}); status != 200 {
		t.Fatalf("desk payment: %d", status)
	}
	if to, _ := waitForReceipt(t, f); to != "somchai@example.com" {
		t.Fatalf("receipt went to %q", to)
	}
}
