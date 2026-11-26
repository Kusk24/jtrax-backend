// Tournament registration, results and payment routes.
package tournaments

import (
	"database/sql"

	"github.com/Kusk24/jtrax-backend/internal/httpx"
	"github.com/Kusk24/jtrax-backend/internal/notify"
	"github.com/Kusk24/jtrax-backend/internal/payments"
	"github.com/Kusk24/jtrax-backend/internal/stripepay"
)

// mountStripe wires the two endpoints. A nil client means card checkout is
// switched off: the link endpoint says so, and the webhook route is not
// registered at all — an unverifiable webhook is better refused by 404 than
// accepted by accident.
func mountStripe(mux httpx.RouteRegistrar, d *sql.DB, client stripepay.CheckoutGateway, cfg stripepay.Config, svc *notify.Service) {
	mux.HandleFunc("POST /api/v1/payments/{id}/stripe-link", payments.HandleStripeLink(d, client, cfg))
	// The parent portal's door to the same thing, for a tournament entry fee.
	// Authenticated, so it sits outside the unauthenticated budget, but it
	// spends a Stripe API call per request and opens a payment row on first
	// ask — a tighter budget than a read deserves.
	mux.HandleFunc("POST /api/v1/tournament-registrations/{id}/stripe-link",
		httpx.RateLimit(20, handleRegistrationStripeLink(d, client, cfg)))
	if client != nil && cfg.WebhookSecret != "" {
		// Unauthenticated by nature — Stripe is not a signed-in user — so it
		// carries the standard unauthenticated-route budget on top of the
		// signature check.
		mux.HandleFunc("POST /api/v1/stripe/webhook", httpx.RateLimit(120, payments.HandleStripeWebhook(d, cfg.WebhookSecret, svc)))
	}

	// Where Checkout sends the parent afterwards. Plain pages with no session,
	// no script and no data: the payment's state is what the webhook said, not
	// which of these two URLs a browser happened to load.
	mux.HandleFunc("GET /pay/done", httpx.RateLimit(60, payments.HandlePayDone(d, client, svc,
		"Payment received — thank you! · ชำระเงินเรียบร้อยแล้ว ขอบคุณค่ะ")))
	// A tournament entry's cancel page says what happens to the unpaid place
	// (entrynotice.go); any other payment's says nothing was charged.
	mux.HandleFunc("GET /pay/cancelled", httpx.RateLimit(60, handlePayCancelled(d, svc, client != nil)))
}
