package api_test

/* Paying for a public entry: the code handed back on registration and put in
   the confirmation email is the only thing that opens that entry's payment. */

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Kusk24/jtrax-backend/internal/api"
	"github.com/Kusk24/jtrax-backend/internal/mail"
	"github.com/Kusk24/jtrax-backend/internal/stripepay"
)

type payFixture struct {
	d      *sql.DB
	srv    *httptest.Server
	pub    *client
	mail   *captureSender
	stripe *[]string
	tourID string
}

// publicPayServer stands up the API with Stripe pointed at a stub (or off), a
// mail sender that captures, and one event open to the public at ฿500.
func publicPayServer(t *testing.T, withStripe bool) *payFixture {
	t.Helper()
	f := &payFixture{d: newDB(t), mail: &captureSender{}}
	if withStripe {
		stub, bodies := stripeStub(t)
		f.stripe = bodies
		t.Setenv("STRIPE_SECRET_KEY", "sk_test_key")
		t.Setenv("STRIPE_WEBHOOK_SECRET", webhookSecret)
		t.Setenv("STRIPE_BASE_URL", stub.URL)
	} else {
		t.Setenv("STRIPE_SECRET_KEY", "")
	}
	cfg := mail.Config{AppURL: "https://portal.example"}
	f.srv = httptest.NewServer(api.NewHandlerWith(f.d, cfg, f.mail, nil))
	t.Cleanup(f.srv.Close)

	staff := &client{t: t, srv: f.srv}
	staff.login("admin@jca.ac.th")
	status, tour, _ := staff.do("POST", "/api/v1/tournaments", map[string]any{
		"name": "JCA Open", "tournament_status": "Upcoming",
		"public_registration": true, "regular_fee": 500,
	})
	if status != 201 {
		t.Fatalf("create tournament: %d (%v)", status, tour)
	}
	f.tourID = tour["tournament_id"].(string)
	f.pub = &client{t: t, srv: f.srv}
	return f
}

// register makes one public entry and returns its id and pay code.
func (f *payFixture) register(t *testing.T, email string) (string, string, map[string]any) {
	t.Helper()
	status, out, _ := f.pub.do("POST", "/api/v1/public/tournaments/"+f.tourID+"/register",
		entry(map[string]any{"email": email}))
	if status != 201 {
		t.Fatalf("register: %d (%v)", status, out)
	}
	id, _ := out["registrationId"].(string)
	code, _ := out["payCode"].(string)
	return id, code, out
}

// waitForMail waits for the confirmation, which is sent off the request.
func (f *payFixture) waitForMail(t *testing.T) (string, string) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if to, body := f.mail.last(); to != "" {
			return to, body
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("no confirmation email was sent")
	return "", ""
}

func randomCode() string {
	b := make([]byte, 32)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func TestAPublicEntrantIsEmailedAPayLink(t *testing.T) {
	f := publicPayServer(t, true)
	id, code, out := f.register(t, "somchai@example.com")

	if len(code) != 64 || id == "" {
		t.Fatalf("want an entry id and a 64-character code, got %q %q", id, code)
	}
	if out["cardPayments"] != true || out["emailed"] != true {
		t.Fatalf("the done screen should offer card payment and say it emailed: %v", out)
	}

	to, body := f.waitForMail(t)
	if to != "somchai@example.com" {
		t.Fatalf("confirmation went to %q", to)
	}
	want := "https://portal.example/register/" + f.tourID + "/pay?entry=" + id + "#code=" + code
	if !strings.Contains(body, want) {
		t.Fatalf("email is missing the pay link %s:\n%s", want, body)
	}
	if !strings.Contains(body, "500 THB") {
		t.Fatalf("email should state the fee:\n%s", body)
	}

	// Only the hash is kept: the column must not be the code itself.
	var stored string
	f.d.QueryRow(`SELECT pay_code_hash FROM tournament_registration
	               WHERE tournament_registration_id = ?`, id).Scan(&stored)
	sum := sha256.Sum256([]byte(code))
	if stored == code || stored != hex.EncodeToString(sum[:]) {
		t.Fatalf("stored %q; want the SHA-256 of the code, never the code", stored)
	}
}

func TestThePayLinkOpensOneStripePageAtTheStoredFee(t *testing.T) {
	f := publicPayServer(t, true)
	id, code, _ := f.register(t, "somchai@example.com")
	base := "/api/v1/public/tournament-registrations/" + id

	status, e, _ := f.pub.do("POST", base, map[string]string{"code": code})
	if status != 200 || e["state"] != "unpaid" || e["fee"] != float64(500) || e["cardPayments"] != true {
		t.Fatalf("entry: %d %v", status, e)
	}

	status, first, _ := f.pub.do("POST", base+"/pay", map[string]string{"code": code})
	if status != 200 || first["url"] == "" {
		t.Fatalf("pay: %d %v", status, first)
	}
	// Opening the email twice must not open a second chargeable page.
	_, second, _ := f.pub.do("POST", base+"/pay", map[string]string{"code": code})
	if second["url"] != first["url"] || len(*f.stripe) != 1 {
		t.Fatalf("second ask made another session: %v vs %v (%d calls)", second["url"], first["url"], len(*f.stripe))
	}
	sent := (*f.stripe)[0]
	for _, want := range []string{"unit_amount%5D=50000", "customer_email=somchai%40example.com"} {
		if !strings.Contains(sent, want) {
			t.Errorf("Stripe request missing %q:\n%s", want, sent)
		}
	}

	// The payment names the entrant and points at no student: the academy has
	// no record of them.
	var student sql.NullString
	var name string
	if err := f.d.QueryRow(`SELECT student_id, student_name FROM payment
	                         WHERE tournament_registration_id = ?`, id).Scan(&student, &name); err != nil {
		t.Fatal(err)
	}
	if student.Valid || name != "Somchai Niran" {
		t.Fatalf("payment: student %v, name %q", student, name)
	}
}

func TestAWrongCodeOpensNothing(t *testing.T) {
	f := publicPayServer(t, true)
	id, _, _ := f.register(t, "somchai@example.com")
	_, otherCode, _ := f.register(t, "other@example.com")
	base := "/api/v1/public/tournament-registrations/" + id

	for _, bad := range []string{"", "short", randomCode(), otherCode} {
		for _, path := range []string{base, base + "/pay"} {
			if status, _, _ := f.pub.do("POST", path, map[string]string{"code": bad}); status != 404 {
				t.Errorf("%s with code %q: want 404, got %d", path, bad, status)
			}
		}
	}
	// A made-up entry id answers the same as a wrong code.
	if status, _, _ := f.pub.do("POST", "/api/v1/public/tournament-registrations/treg_nope/pay",
		map[string]string{"code": randomCode()}); status != 404 {
		t.Errorf("unknown entry: want 404, got %d", status)
	}
	if len(*f.stripe) != 0 {
		t.Fatalf("a wrong code reached Stripe %d times", len(*f.stripe))
	}
}

func TestAPaidEntryIsNotChargedAgain(t *testing.T) {
	f := publicPayServer(t, true)
	id, code, _ := f.register(t, "somchai@example.com")
	base := "/api/v1/public/tournament-registrations/" + id
	f.pub.do("POST", base+"/pay", map[string]string{"code": code})

	var payID string
	f.d.QueryRow(`SELECT payment_id FROM payment WHERE tournament_registration_id = ?`, id).Scan(&payID)
	payload := completedEvent("cs_1", payID, 50000)
	if got := postWebhook(t, f.srv, payload, stripepay.Sign(payload, webhookSecret, time.Now())); got != 200 {
		t.Fatalf("webhook: %d", got)
	}

	if _, e, _ := f.pub.do("POST", base, map[string]string{"code": code}); e["state"] != "paid" {
		t.Fatalf("after paying: %v", e)
	}
	if status, _, _ := f.pub.do("POST", base+"/pay", map[string]string{"code": code}); status != 409 {
		t.Fatalf("paying a paid entry: want 409, got %d", status)
	}
}

func TestWithoutStripeTheEmailSaysHowElseToPay(t *testing.T) {
	f := publicPayServer(t, false)
	id, code, out := f.register(t, "somchai@example.com")
	if out["cardPayments"] != false {
		t.Fatalf("card payment offered with no Stripe key: %v", out)
	}
	_, body := f.waitForMail(t)
	if strings.Contains(body, "#code=") || !strings.Contains(body, "bank transfer") {
		t.Fatalf("with card payments off the email should say bank transfer and carry no link:\n%s", body)
	}
	if status, _, _ := f.pub.do("POST", "/api/v1/public/tournament-registrations/"+id+"/pay",
		map[string]string{"code": code}); status != 503 {
		t.Fatalf("pay with Stripe off: want 503, got %d", status)
	}
}
