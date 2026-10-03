package api_test

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Kusk24/jtrax-backend/internal/api"
	"github.com/Kusk24/jtrax-backend/internal/mail"
	"github.com/Kusk24/jtrax-backend/internal/notify"
	"github.com/Kusk24/jtrax-backend/internal/stripepay"
)

// sessionStub plays Stripe for both calls: opening a session, and reading one
// back in whatever state the test has put it.
type sessionStub struct {
	mu      sync.Mutex
	opened  int
	states  map[string]string // session id -> JSON body of GET /v1/checkout/sessions/{id}
	created []string          // success_url of every session opened
}

func (s *sessionStub) set(id, paymentID, status, paymentStatus string, satang int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.states[id] = fmt.Sprintf(`{"id":%q,"status":%q,"payment_status":%q,"amount_total":%d,"currency":"thb","metadata":{"payment_id":%q}}`,
		id, status, paymentStatus, satang, paymentID)
}

func newSessionStub(t *testing.T) (*httptest.Server, *sessionStub) {
	t.Helper()
	st := &sessionStub{states: map[string]string{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		st.mu.Lock()
		defer st.mu.Unlock()
		if r.Method == http.MethodPost {
			r.ParseForm()
			st.opened++
			st.created = append(st.created, r.PostForm.Get("success_url"))
			fmt.Fprintf(w, `{"id":"cs_%d","url":"https://checkout.stripe.test/cs_%d"}`, st.opened, st.opened)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/v1/checkout/sessions/")
		body, ok := st.states[id]
		if !ok {
			// Opened, never touched: what Stripe says of a page nobody paid on.
			body = fmt.Sprintf(`{"id":%q,"status":"open","payment_status":"unpaid"}`, id)
		}
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("STRIPE_SECRET_KEY", "sk_test_key")
	t.Setenv("STRIPE_WEBHOOK_SECRET", webhookSecret)
	t.Setenv("STRIPE_BASE_URL", srv.URL)
	return srv, st
}

// openCardPayment makes a pending ฿500 payment and opens its Stripe page,
// returning the payment and the session id the server stored.
func openCardPayment(t *testing.T, d *sql.DB) (*httptest.Server, string, string) {
	t.Helper()
	srv := httptest.NewServer(api.NewHandler(d))
	t.Cleanup(srv.Close)
	c := &client{t: t, srv: srv}
	c.login("admin@jca.ac.th")
	pay := pendingPayment(t, c, 500)
	if status, obj, _ := c.do("POST", "/api/v1/payments/"+pay+"/stripe-link", nil); status != 200 {
		t.Fatalf("opening the card page: %d (%v)", status, obj)
	}
	var session string
	d.QueryRow(`SELECT stripe_session_id FROM payment WHERE payment_id = ?`, pay).Scan(&session)
	if session == "" {
		t.Fatal("no session stored")
	}
	return srv, pay, session
}

func paymentStatus(t *testing.T, d *sql.DB, id string) string {
	t.Helper()
	var s string
	if err := d.QueryRow(`SELECT status FROM payment WHERE payment_id = ?`, id).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

func reconcile(t *testing.T, d *sql.DB) {
	t.Helper()
	svc := notify.New(d, nil, mail.Config{})
	if err := api.ReconcileStripePayments(context.Background(), d,
		stripepay.New(stripepay.FromEnv()), svc); err != nil {
		t.Fatal(err)
	}
}

func TestAPaymentTheWebhookNeverReportedIsSettledByAskingStripe(t *testing.T) {
	d := newDB(t)
	_, st := newSessionStub(t)
	_, pay, session := openCardPayment(t, d)

	reconcile(t, d)
	if got := paymentStatus(t, d, pay); got != "Pending" {
		t.Fatalf("before paying: %s, want Pending", got)
	}

	// PromptPay: the page completes before the bank confirms.
	st.set(session, pay, "complete", "unpaid", 50000)
	reconcile(t, d)
	if got := paymentStatus(t, d, pay); got != "Pending" {
		t.Fatalf("PromptPay still confirming: %s, want Pending", got)
	}

	st.set(session, pay, "complete", "paid", 50000)
	reconcile(t, d)
	if got := paymentStatus(t, d, pay); got != "Paid" {
		t.Fatalf("after Stripe collected it: %s, want Paid", got)
	}
	// A second pass, or the webhook arriving late, changes nothing.
	reconcile(t, d)
	var credits int
	d.QueryRow(`SELECT COUNT(*) FROM credit_transaction WHERE payment_id = ?`, pay).Scan(&credits)
	if credits != 1 {
		t.Fatalf("credit grants after two passes: %d, want 1", credits)
	}
}

func TestReconcilingRefusesAnAmountThatDoesNotMatch(t *testing.T) {
	d := newDB(t)
	_, st := newSessionStub(t)
	_, pay, session := openCardPayment(t, d)

	st.set(session, pay, "complete", "paid", 1000) // ฿10 against a ฿500 debt
	reconcile(t, d)
	if got := paymentStatus(t, d, pay); got != "Pending" {
		t.Fatalf("after a ฿10 session: %s, want Pending", got)
	}
}

func TestReturningFromStripeSettlesThePayment(t *testing.T) {
	d := newDB(t)
	_, st := newSessionStub(t)
	srv, pay, session := openCardPayment(t, d)

	if len(st.created) != 1 || !strings.Contains(st.created[0], "/pay/done?session_id={CHECKOUT_SESSION_ID}") {
		t.Fatalf("success url %v does not ask Stripe for the session id", st.created)
	}

	// A made-up session id settles nothing: the page only acts on Stripe's word.
	res, err := http.Get(srv.URL + "/pay/done?session_id=cs_forged")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 || paymentStatus(t, d, pay) != "Pending" {
		t.Fatalf("forged return: %d, %s", res.StatusCode, paymentStatus(t, d, pay))
	}

	st.set(session, pay, "complete", "paid", 50000)
	res, err = http.Get(srv.URL + "/pay/done?session_id=" + session)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if got := paymentStatus(t, d, pay); got != "Paid" {
		t.Fatalf("after returning from Stripe: %s, want Paid", got)
	}
}

func TestAnExpiredStripePageIsReplacedOnTheNextAsk(t *testing.T) {
	d := newDB(t)
	_, st := newSessionStub(t)
	srv, pay, session := openCardPayment(t, d)

	st.set(session, pay, "expired", "unpaid", 50000)
	reconcile(t, d)

	c := &client{t: t, srv: srv}
	c.login("admin@jca.ac.th")
	_, obj, _ := c.do("POST", "/api/v1/payments/"+pay+"/stripe-link", nil)
	if url, _ := obj["url"].(string); strings.HasSuffix(url, "/"+session) {
		t.Fatalf("handed back the expired page %s", url)
	}
}
