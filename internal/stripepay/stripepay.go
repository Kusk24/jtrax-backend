// Package stripepay creates Stripe Checkout sessions and verifies the webhooks
// that report them paid.
//
// Checkout is the hosted flow: the parent types their card into Stripe's page,
// not ours. No card number, expiry, CVC or holder name ever reaches this
// server, which is what keeps the academy out of PCI scope — the only Stripe
// data JTrax holds is a session id and a yes/no.
//
// Hand-rolled HTTP rather than the stripe-go SDK for the same reason the OCR
// provider is: two calls do not justify a dependency tree, and everything that
// matters for safety — the signature check, the amount check — is spelled out
// here where it can be read.
package stripepay

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is read from the environment only. Both keys are credentials: never
// committed, never logged, never returned by an endpoint.
type Config struct {
	// SecretKey is the sk_test_… / sk_live_… API key. Empty means card
	// checkout is switched off, which is a supported state.
	SecretKey string
	// WebhookSecret is the whsec_… signing secret for the endpoint registered
	// in the Stripe dashboard.
	WebhookSecret string
	// BaseURL overrides https://api.stripe.com. For pointing a development
	// machine at a local stub; production leaves it unset.
	BaseURL string
	// ReturnURL is where Checkout sends the parent afterwards. Defaults to
	// PUBLIC_API_URL, where this server serves a plain thank-you page.
	ReturnURL string
}

func FromEnv() Config {
	return Config{
		SecretKey:     os.Getenv("STRIPE_SECRET_KEY"),
		WebhookSecret: os.Getenv("STRIPE_WEBHOOK_SECRET"),
		BaseURL:       os.Getenv("STRIPE_BASE_URL"),
		ReturnURL:     os.Getenv("STRIPE_RETURN_URL"),
	}
}

// Client talks to Stripe. Nil is the switched-off state: endpoints answer 503
// rather than 500, and the rest of JTrax is unaffected.
type Client struct {
	key  string
	base string
	http *http.Client
}

func New(c Config) *Client {
	if strings.TrimSpace(c.SecretKey) == "" {
		return nil
	}
	base := strings.TrimSuffix(c.BaseURL, "/")
	if base == "" {
		base = "https://api.stripe.com"
	}
	return &Client{key: c.SecretKey, base: base, http: &http.Client{Timeout: 20 * time.Second}}
}

// Session is the slice of a Checkout Session this server cares about.
type Session struct {
	ID  string
	URL string
}

// CreateCheckoutSession opens a hosted card-payment page for one payment row.
// The amount is in satang — Stripe takes THB in its minor unit — and the
// payment id rides in the metadata so the webhook can find its way back.
// customerEmail, when known, is filled in on the page so the payer does not
// type it twice; empty leaves the box for them.
//
// expiresAt, when set, closes the page early (Stripe allows 30 minutes to 24
// hours; zero leaves Stripe's 24). A tournament entry uses it so a family
// who walks away hears "your payment was not completed" within the hour.
func (c *Client) CreateCheckoutSession(ctx context.Context, paymentID, productName string, amountSatang int64, successURL, cancelURL, customerEmail string, expiresAt time.Time) (Session, error) {
	form := url.Values{}
	if customerEmail != "" {
		form.Set("customer_email", customerEmail)
	}
	if !expiresAt.IsZero() {
		form.Set("expires_at", strconv.FormatInt(expiresAt.Unix(), 10))
	}
	form.Set("mode", "payment")
	form.Set("client_reference_id", paymentID)
	form.Set("metadata[payment_id]", paymentID)
	form.Set("line_items[0][quantity]", "1")
	form.Set("line_items[0][price_data][currency]", "thb")
	form.Set("line_items[0][price_data][unit_amount]", strconv.FormatInt(amountSatang, 10))
	form.Set("line_items[0][price_data][product_data][name]", productName)
	form.Set("success_url", successURL)
	form.Set("cancel_url", cancelURL)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/v1/checkout/sessions", strings.NewReader(form.Encode()))
	if err != nil {
		return Session{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Bearer "+c.key)

	res, err := c.http.Do(req)
	if err != nil {
		return Session{}, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return Session{}, err
	}
	if res.StatusCode != http.StatusOK {
		// Stripe's error body names the account and the request; it is for the
		// server log, never for a client.
		return Session{}, fmt.Errorf("stripe: checkout session: %s: %s", res.Status, truncate(body, 300))
	}
	var s struct {
		ID  string `json:"id"`
		URL string `json:"url"`
	}
	if err := json.Unmarshal(body, &s); err != nil {
		return Session{}, fmt.Errorf("stripe: decoding session: %w", err)
	}
	if s.ID == "" || s.URL == "" {
		return Session{}, errors.New("stripe: session response missing id or url")
	}
	return Session{ID: s.ID, URL: s.URL}, nil
}

// SessionState is what Stripe says about a Checkout session right now.
type SessionState struct {
	ID string
	// Status is "open", "complete" or "expired".
	Status string
	// PaymentStatus is "paid" once the money is collected. A PromptPay
	// session is "complete" but "unpaid" until the bank confirms.
	PaymentStatus string
	AmountTotal   int64
	Currency      string
	// PaymentID is the metadata this server set when it opened the session.
	PaymentID string
}

// GetCheckoutSession asks Stripe for a session's current state. It is the
// same answer the webhook carries, fetched by this server from Stripe itself
// with the secret key — so, unlike anything a browser sends, it can be trusted
// without a signature.
func (c *Client) GetCheckoutSession(ctx context.Context, id string) (SessionState, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.base+"/v1/checkout/sessions/"+url.PathEscape(id), nil)
	if err != nil {
		return SessionState{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.key)

	res, err := c.http.Do(req)
	if err != nil {
		return SessionState{}, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return SessionState{}, err
	}
	if res.StatusCode != http.StatusOK {
		return SessionState{}, fmt.Errorf("stripe: reading session: %s: %s", res.Status, truncate(body, 300))
	}
	var s struct {
		ID            string            `json:"id"`
		Status        string            `json:"status"`
		PaymentStatus string            `json:"payment_status"`
		AmountTotal   int64             `json:"amount_total"`
		Currency      string            `json:"currency"`
		Metadata      map[string]string `json:"metadata"`
	}
	if err := json.Unmarshal(body, &s); err != nil {
		return SessionState{}, fmt.Errorf("stripe: decoding session: %w", err)
	}
	return SessionState{
		ID: s.ID, Status: s.Status, PaymentStatus: s.PaymentStatus,
		AmountTotal: s.AmountTotal, Currency: s.Currency, PaymentID: s.Metadata["payment_id"],
	}, nil
}

// ExpireCheckoutSession closes a page that is still open, so it can no longer
// take money — for an entry whose place has been released. Stripe refuses
// to expire a page that is already complete or expired; that comes back as
// an error the caller may ignore.
func (c *Client) ExpireCheckoutSession(ctx context.Context, id string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.base+"/v1/checkout/sessions/"+url.PathEscape(id)+"/expire", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	res, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("stripe: expiring session: %s: %s", res.Status, truncate(body, 300))
	}
	return nil
}

// Tolerance is how stale a webhook's timestamp may be. Five minutes is
// Stripe's own recommendation; anything older is a replay.
const Tolerance = 5 * time.Minute

// ErrBadSignature covers every way a webhook can fail verification. One error
// on purpose: the response must not teach a forger which part was wrong.
var ErrBadSignature = errors.New("stripe: webhook signature verification failed")

// VerifyWebhook checks a Stripe-Signature header against the payload.
//
// The scheme is Stripe's: the header carries `t=<unix>,v1=<hex hmac>`, the
// signed message is "<t>.<payload>", the key is the endpoint's signing secret.
// The timestamp bounds replays; hmac.Equal keeps the comparison constant-time.
// Multiple v1 entries are legal (they appear while a secret is being rolled)
// and any one matching is a pass.
func VerifyWebhook(payload []byte, header, secret string, now time.Time) error {
	if secret == "" || header == "" {
		return ErrBadSignature
	}
	var ts int64 = -1
	var sigs [][]byte
	for _, part := range strings.Split(header, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		switch k {
		case "t":
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				return ErrBadSignature
			}
			ts = n
		case "v1":
			sig, err := hex.DecodeString(v)
			if err == nil {
				sigs = append(sigs, sig)
			}
		}
	}
	if ts < 0 || len(sigs) == 0 {
		return ErrBadSignature
	}
	age := now.Sub(time.Unix(ts, 0))
	if age > Tolerance || age < -Tolerance {
		return ErrBadSignature
	}

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(strconv.FormatInt(ts, 10)))
	mac.Write([]byte("."))
	mac.Write(payload)
	want := mac.Sum(nil)
	for _, sig := range sigs {
		if hmac.Equal(sig, want) {
			return nil
		}
	}
	return ErrBadSignature
}

// Sign produces a valid Stripe-Signature header. Tests use it to be the
// sender; nothing in production calls it.
func Sign(payload []byte, secret string, at time.Time) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(strconv.FormatInt(at.Unix(), 10)))
	mac.Write([]byte("."))
	mac.Write(payload)
	return fmt.Sprintf("t=%d,v1=%s", at.Unix(), hex.EncodeToString(mac.Sum(nil)))
}

func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "…"
}
