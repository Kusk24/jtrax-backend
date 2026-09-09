// Card payments through Stripe Checkout: the desk turns a pending payment
// into a hosted payment link, and the webhook turns "paid on Stripe's page"
// into the same Paid-plus-credits state the desk produces by hand.
package api

import (
	"database/sql"
	"encoding/json"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Kusk24/jtrax-backend/internal/httpx"
	"github.com/Kusk24/jtrax-backend/internal/stripepay"
)

// Stripe's minimum charge for THB, in satang (฿10). Below it the API refuses,
// so refuse here first with a message a person can act on.
const minSatang = 1000

// mountStripe wires the two endpoints. A nil client means card checkout is
// switched off: the link endpoint says so, and the webhook route is not
// registered at all — an unverifiable webhook is better refused by 404 than
// accepted by accident.
func mountStripe(mux *http.ServeMux, d *sql.DB, client *stripepay.Client, cfg stripepay.Config) {
	mux.HandleFunc("POST /api/v1/payments/{id}/stripe-link", handleStripeLink(d, client, cfg))
	if client != nil && cfg.WebhookSecret != "" {
		// Unauthenticated by nature — Stripe is not a signed-in user — so it
		// carries the standard unauthenticated-route budget on top of the
		// signature check.
		mux.HandleFunc("POST /api/v1/stripe/webhook", httpx.RateLimit(120, handleStripeWebhook(d, cfg.WebhookSecret)))
	}

	// Where Checkout sends the parent afterwards. Plain pages with no session,
	// no script and no data: the payment's state is what the webhook said, not
	// which of these two URLs a browser happened to load.
	mux.HandleFunc("GET /pay/done", payPage("Payment received — thank you! · ชำระเงินเรียบร้อยแล้ว ขอบคุณค่ะ"))
	mux.HandleFunc("GET /pay/cancelled", payPage("Payment cancelled — nothing was charged. · ยกเลิกการชำระเงิน ยังไม่มีการตัดเงิน"))
}

func payPage(text string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<!doctype html><meta name=viewport content=\"width=device-width,initial-scale=1\">" +
			"<title>JCA Chess Academy</title>" +
			"<body style=\"font-family:sans-serif;display:flex;min-height:90vh;align-items:center;justify-content:center;text-align:center;padding:24px\">" +
			"<p style=\"font-size:19px;max-width:26em\">" + text + "</p>"))
	}
}

// returnBase is where the thank-you pages live: the public API origin in
// production, this server itself in development.
func returnBase(cfg stripepay.Config) string {
	if cfg.ReturnURL != "" {
		return strings.TrimSuffix(cfg.ReturnURL, "/")
	}
	if base := strings.TrimSuffix(strings.TrimSpace(os.Getenv("PUBLIC_API_URL")), "/"); base != "" {
		return base
	}
	return "http://localhost:8790"
}

// handleStripeLink answers with a card-payment URL for one pending payment,
// creating the Checkout session on first ask and returning the stored one
// after — closing the tab must not mean a second link and a double charge.
func handleStripeLink(d *sql.DB, client *stripepay.Client, cfg stripepay.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := requireIdentity(d, w, r)
		if id == nil {
			return
		}
		if !isStaff(id.Role) {
			httpx.Error(w, http.StatusForbidden, "only admin or reception may create payment links", nil)
			return
		}
		if client == nil {
			httpx.Error(w, http.StatusServiceUnavailable, "card payments are not configured", nil)
			return
		}

		paymentID := r.PathValue("id")
		var status, studentName, className, existingURL string
		var finalAmount float64
		err := d.QueryRow(
			`SELECT status, COALESCE(student_name,''), COALESCE(class_name,''),
			        final_amount, COALESCE(stripe_checkout_url,'')
			   FROM payment WHERE payment_id = ?`, paymentID).
			Scan(&status, &studentName, &className, &finalAmount, &existingURL)
		if err == sql.ErrNoRows {
			httpx.Error(w, http.StatusNotFound, "no such payment", nil)
			return
		}
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not read payment", err)
			return
		}
		// Only money still owed gets a link. A Paid payment has nothing to
		// collect and a Refunded one must never become chargeable again.
		if status != "Pending" {
			httpx.Error(w, http.StatusConflict, "only a pending payment can take a card link", nil)
			return
		}
		if existingURL != "" {
			httpx.JSON(w, http.StatusOK, map[string]string{"url": existingURL})
			return
		}
		satang := int64(math.Round(finalAmount * 100))
		if satang < minSatang {
			httpx.Error(w, http.StatusUnprocessableEntity, "amount is below the ฿10 card minimum", nil)
			return
		}

		name := "JCA Chess Academy"
		if className != "" {
			name += " — " + className
		}
		if studentName != "" {
			name += " (" + studentName + ")"
		}
		base := returnBase(cfg)
		session, err := client.CreateCheckoutSession(r.Context(), paymentID, name, satang,
			base+"/pay/done", base+"/pay/cancelled")
		if err != nil {
			// The Stripe error names the account; the log gets it, the client
			// does not.
			httpx.Error(w, http.StatusBadGateway, "could not create the payment link", err)
			return
		}
		if _, err := d.Exec(
			`UPDATE payment SET stripe_session_id = ?, stripe_checkout_url = ? WHERE payment_id = ?`,
			session.ID, session.URL, paymentID); err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not store the payment link", err)
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]string{"url": session.URL})
	}
}

// stripeEvent is the slice of a webhook event this server reads.
type stripeEvent struct {
	Type string `json:"type"`
	Data struct {
		Object struct {
			ID            string            `json:"id"`
			PaymentStatus string            `json:"payment_status"`
			AmountTotal   int64             `json:"amount_total"`
			Currency      string            `json:"currency"`
			Metadata      map[string]string `json:"metadata"`
		} `json:"object"`
	} `json:"data"`
}

// handleStripeWebhook marks a payment Paid — and releases its credits — when
// Stripe reports its Checkout session completed.
//
// Everything here assumes the caller is hostile until the signature says
// otherwise, and assumes Stripe will deliver the same event more than once,
// because it will. The idempotency guard is the UPDATE's `status = 'Pending'`:
// whichever delivery flips the row grants the credits, every other one changes
// nothing and answers 200.
func handleStripeWebhook(d *sql.DB, secret string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		payload, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 512*1024))
		if err != nil {
			httpx.Error(w, http.StatusRequestEntityTooLarge, "payload too large", nil)
			return
		}
		if err := stripepay.VerifyWebhook(payload, r.Header.Get("Stripe-Signature"), secret, time.Now()); err != nil {
			// One message for every failure mode, so a forger learns nothing.
			httpx.Error(w, http.StatusBadRequest, "invalid signature", nil)
			return
		}

		var ev stripeEvent
		if err := json.Unmarshal(payload, &ev); err != nil {
			httpx.Error(w, http.StatusBadRequest, "malformed event", nil)
			return
		}
		// Everything else Stripe can send — expiries, refbacks, the async
		// events of methods the academy does not take — is acknowledged and
		// dropped. Answering non-200 would just make Stripe resend it.
		if ev.Type != "checkout.session.completed" || ev.Data.Object.PaymentStatus != "paid" {
			httpx.JSON(w, http.StatusOK, map[string]string{"received": "ignored"})
			return
		}
		paymentID := ev.Data.Object.Metadata["payment_id"]
		if paymentID == "" {
			httpx.JSON(w, http.StatusOK, map[string]string{"received": "no payment id"})
			return
		}

		tx, err := d.Begin()
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not open transaction", err)
			return
		}
		defer tx.Rollback()

		var finalAmount float64
		err = tx.QueryRow(`SELECT final_amount FROM payment WHERE payment_id = ?`, paymentID).Scan(&finalAmount)
		if err == sql.ErrNoRows {
			// Not ours — perhaps a test-mode event against a live database.
			// Acknowledged so Stripe stops retrying; logged so a person looks.
			log.Printf("stripe: webhook for unknown payment %q", paymentID)
			httpx.JSON(w, http.StatusOK, map[string]string{"received": "unknown payment"})
			return
		}
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not read payment", err)
			return
		}
		// The amount Stripe collected must be the amount this payment asked
		// for. A mismatch means the session was not one this server created —
		// refuse loudly rather than mark a ฿9,000 debt settled by ฿10.
		if ev.Data.Object.AmountTotal != int64(math.Round(finalAmount*100)) ||
			!strings.EqualFold(ev.Data.Object.Currency, "thb") {
			log.Printf("stripe: amount mismatch on %s: event %d %s, payment %.2f THB",
				paymentID, ev.Data.Object.AmountTotal, ev.Data.Object.Currency, finalAmount)
			httpx.Error(w, http.StatusBadRequest, "amount mismatch", nil)
			return
		}

		res, err := tx.Exec(
			`UPDATE payment SET status = 'Paid', payment_method = 'CreditCard',
			        stripe_session_id = ?
			  WHERE payment_id = ? AND status = 'Pending'`,
			ev.Data.Object.ID, paymentID)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not update payment", err)
			return
		}
		if n, _ := res.RowsAffected(); n == 0 {
			// Already Paid (a retry, or the desk beat the webhook) — done.
			httpx.JSON(w, http.StatusOK, map[string]string{"received": "already settled"})
			return
		}
		if err := grantPurchasedCredits(tx, paymentID); err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not grant credits", err)
			return
		}
		if err := tx.Commit(); err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not commit", err)
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]string{"received": "ok"})
	}
}

// grantPurchasedCredits writes the credit purchase behind a payment, exactly
// as the console does when the desk marks one Paid by hand: same amount, same
// validity window, same link back to the payment. A payment without a package
// or an enrolment legitimately buys nothing (a tournament fee, say).
func grantPurchasedCredits(tx *sql.Tx, paymentID string) error {
	var enrollmentID, packageID, studentID sql.NullString
	err := tx.QueryRow(
		`SELECT enrollment_id, credit_package_id, student_id FROM payment WHERE payment_id = ?`,
		paymentID).Scan(&enrollmentID, &packageID, &studentID)
	if err != nil {
		return err
	}
	if !enrollmentID.Valid || enrollmentID.String == "" || !packageID.Valid || packageID.String == "" {
		return nil
	}
	var classID sql.NullString
	var creditAmount float64
	var validityDays int
	err = tx.QueryRow(
		`SELECT class_id, credit_amount, COALESCE(validity_days, 0)
		   FROM credit_package WHERE credit_package_id = ?`, packageID.String).
		Scan(&classID, &creditAmount, &validityDays)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}

	day := today()
	var expiry any
	if validityDays > 0 {
		t, err := time.Parse("2006-01-02", day)
		if err != nil {
			return err
		}
		expiry = t.AddDate(0, 0, validityDays).Format("2006-01-02")
	}
	_, err = tx.Exec(
		`INSERT INTO credit_transaction
		        (credit_transaction_id, enrollment_id, student_id, class_id,
		         transaction_type, amount, transaction_date, expiry_date, payment_id, notes)
		 VALUES (?,?,?,?,?,?,?,?,?,?)`,
		newID("ctx"), enrollmentID.String, nullable(studentID.String), nullable(classID.String),
		"purchase", creditAmount, day, expiry, paymentID, "card payment")
	return err
}
