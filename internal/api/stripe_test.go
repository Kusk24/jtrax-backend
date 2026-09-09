package api_test

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Kusk24/jtrax-backend/internal/api"
	"github.com/Kusk24/jtrax-backend/internal/stripepay"
)

const webhookSecret = "whsec_test_secret"

// stripeStub plays Stripe's API: every session request answers with a fixed
// checkout URL and remembers what was asked for.
func stripeStub(t *testing.T) (*httptest.Server, *[]string) {
	t.Helper()
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := make([]byte, r.ContentLength)
		r.Body.Read(b)
		bodies = append(bodies, string(b))
		fmt.Fprintf(w, `{"id":"cs_%d","url":"https://checkout.stripe.test/cs_%d"}`, len(bodies), len(bodies))
	}))
	t.Cleanup(srv.Close)
	return srv, &bodies
}

// newStripeServer stands the API up with Stripe pointed at the stub. Env vars
// because that is the only way production configures it — the test exercises
// the same path.
func newStripeServer(t *testing.T, d *sql.DB) *httptest.Server {
	t.Helper()
	stub, _ := stripeStub(t)
	t.Setenv("STRIPE_SECRET_KEY", "sk_test_key")
	t.Setenv("STRIPE_WEBHOOK_SECRET", webhookSecret)
	t.Setenv("STRIPE_BASE_URL", stub.URL)
	srv := httptest.NewServer(api.NewHandler(d))
	t.Cleanup(srv.Close)
	return srv
}

// pendingPayment writes a pending card-sized payment for Penny and returns its id.
func pendingPayment(t *testing.T, c *client, amount float64) string {
	t.Helper()
	status, obj, _ := c.do("POST", "/api/v1/payments", map[string]any{
		"student_id": "stu_penny", "enrollment_id": "enr_penny",
		"credit_package_id": "pkg_beg20", "student_name": "Penny",
		"class_name": "Beginner Chess (Sec 101)", "amount": amount,
		"final_amount": amount, "payment_method": "CreditCard",
		"status": "Pending", "payment_date": "2026-09-08",
	})
	if status != 201 {
		t.Fatalf("creating pending payment: %d (%v)", status, obj)
	}
	return obj["payment_id"].(string)
}

func completedEvent(sessionID, paymentID string, satang int64) []byte {
	ev := map[string]any{
		"type": "checkout.session.completed",
		"data": map[string]any{"object": map[string]any{
			"id": sessionID, "payment_status": "paid",
			"amount_total": satang, "currency": "thb",
			"metadata": map[string]string{"payment_id": paymentID},
		}},
	}
	b, _ := json.Marshal(ev)
	return b
}

func postWebhook(t *testing.T, srv *httptest.Server, payload []byte, header string) int {
	t.Helper()
	req, _ := http.NewRequest("POST", srv.URL+"/api/v1/stripe/webhook", bytes.NewReader(payload))
	if header != "" {
		req.Header.Set("Stripe-Signature", header)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	return res.StatusCode
}

func TestStripeLinkIsStaffOnly(t *testing.T) {
	d := newDB(t)
	srv := newStripeServer(t, d)
	c := &client{t: t, srv: srv}
	c.login("sandy01234@gmail.com") // a parent
	status, _, _ := c.do("POST", "/api/v1/payments/pay_1/stripe-link", nil)
	if status != http.StatusForbidden {
		t.Fatalf("parent creating a payment link: got %d, want 403", status)
	}
}

func TestStripeLinkOffWithoutKeys(t *testing.T) {
	d := newDB(t)
	srv := newServerOn(t, d) // no Stripe env
	c := &client{t: t, srv: srv}
	c.login("admin@jca.ac.th")
	status, _, _ := c.do("POST", "/api/v1/payments/pay_1/stripe-link", nil)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("unconfigured link endpoint: got %d, want 503", status)
	}
	// And the webhook route must not exist at all.
	if got := postWebhook(t, srv, []byte("{}"), ""); got != http.StatusNotFound {
		t.Fatalf("unconfigured webhook: got %d, want 404", got)
	}
}

func TestStripeLinkLifecycle(t *testing.T) {
	d := newDB(t)
	srv := newStripeServer(t, d)
	c := &client{t: t, srv: srv}
	c.login("admin@jca.ac.th")

	// A settled payment cannot take a link.
	status, _, _ := c.do("POST", "/api/v1/payments/pay_1/stripe-link", nil)
	if status != http.StatusConflict {
		t.Fatalf("link on a Paid payment: got %d, want 409", status)
	}

	// Below Stripe's ฿10 floor is refused before Stripe sees it.
	tiny := pendingPayment(t, c, 5)
	status, _, _ = c.do("POST", "/api/v1/payments/"+tiny+"/stripe-link", nil)
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("link under the minimum: got %d, want 422", status)
	}

	// A pending payment gets a link…
	payID := pendingPayment(t, c, 4500)
	status, obj, _ := c.do("POST", "/api/v1/payments/"+payID+"/stripe-link", nil)
	if status != 200 || obj["url"] != "https://checkout.stripe.test/cs_1" {
		t.Fatalf("creating link: %d (%v)", status, obj)
	}
	// …and asking again answers with the same one, not a second session.
	status, obj, _ = c.do("POST", "/api/v1/payments/"+payID+"/stripe-link", nil)
	if status != 200 || obj["url"] != "https://checkout.stripe.test/cs_1" {
		t.Fatalf("re-asking minted a new session: %d (%v)", status, obj)
	}
}

func TestStripeWebhookSettlesOnceAndGrantsCreditsOnce(t *testing.T) {
	d := newDB(t)
	srv := newStripeServer(t, d)
	c := &client{t: t, srv: srv}
	c.login("admin@jca.ac.th")
	payID := pendingPayment(t, c, 12000)
	c.do("POST", "/api/v1/payments/"+payID+"/stripe-link", nil)

	payload := completedEvent("cs_1", payID, 1200000)
	header := stripepay.Sign(payload, webhookSecret, time.Now())

	// Stripe delivers, then redelivers. Same event, same signature.
	if got := postWebhook(t, srv, payload, header); got != 200 {
		t.Fatalf("first delivery: %d", got)
	}
	if got := postWebhook(t, srv, payload, header); got != 200 {
		t.Fatalf("redelivery: %d", got)
	}

	var status, method string
	if err := d.QueryRow(`SELECT status, payment_method FROM payment WHERE payment_id = ?`, payID).
		Scan(&status, &method); err != nil {
		t.Fatal(err)
	}
	if status != "Paid" || method != "CreditCard" {
		t.Fatalf("payment after webhook: %s / %s", status, method)
	}

	// Exactly one credit purchase, with the package's amount and window.
	var n int
	var amount float64
	var expiry string
	if err := d.QueryRow(
		`SELECT COUNT(*), COALESCE(SUM(amount),0), COALESCE(MAX(expiry_date),'')
		   FROM credit_transaction WHERE payment_id = ?`, payID).Scan(&n, &amount, &expiry); err != nil {
		t.Fatal(err)
	}
	if n != 1 || amount != 20 {
		t.Fatalf("credits granted %d times, total %.1f; want once, 20", n, amount)
	}
	want := time.Now().AddDate(0, 0, 120).Format("2006-01-02")
	if expiry != want {
		t.Fatalf("credit expiry %s, want %s (120-day package)", expiry, want)
	}
}

func TestStripeWebhookRefusesWhatItShould(t *testing.T) {
	d := newDB(t)
	srv := newStripeServer(t, d)
	c := &client{t: t, srv: srv}
	c.login("admin@jca.ac.th")
	payID := pendingPayment(t, c, 12000)

	assertStillPending := func(when string) {
		t.Helper()
		var status string
		d.QueryRow(`SELECT status FROM payment WHERE payment_id = ?`, payID).Scan(&status)
		if status != "Pending" {
			t.Fatalf("%s: payment became %s", when, status)
		}
	}

	payload := completedEvent("cs_x", payID, 1200000)

	// No signature, a forged signature, a stale one.
	if got := postWebhook(t, srv, payload, ""); got != http.StatusBadRequest {
		t.Fatalf("unsigned webhook: %d", got)
	}
	if got := postWebhook(t, srv, payload, stripepay.Sign(payload, "whsec_forged", time.Now())); got != http.StatusBadRequest {
		t.Fatalf("forged webhook: %d", got)
	}
	if got := postWebhook(t, srv, payload, stripepay.Sign(payload, webhookSecret, time.Now().Add(-time.Hour))); got != http.StatusBadRequest {
		t.Fatalf("replayed webhook: %d", got)
	}
	assertStillPending("after bad signatures")

	// Correctly signed but the money is wrong: a ฿10 session must not settle a
	// ฿12,000 debt.
	short := completedEvent("cs_x", payID, 1000)
	if got := postWebhook(t, srv, short, stripepay.Sign(short, webhookSecret, time.Now())); got != http.StatusBadRequest {
		t.Fatalf("amount mismatch: %d", got)
	}
	assertStillPending("after amount mismatch")

	// Correctly signed but not a completed checkout.
	unpaid := bytes.Replace(payload, []byte(`"paid"`), []byte(`"unpaid"`), 1)
	if got := postWebhook(t, srv, unpaid, stripepay.Sign(unpaid, webhookSecret, time.Now())); got != 200 {
		t.Fatalf("unpaid session event: %d", got)
	}
	assertStillPending("after unpaid session event")

	var credits int
	d.QueryRow(`SELECT COUNT(*) FROM credit_transaction WHERE payment_id = ?`, payID).Scan(&credits)
	if credits != 0 {
		t.Fatalf("refused webhooks still granted %d credit rows", credits)
	}
}
