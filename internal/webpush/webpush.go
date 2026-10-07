// Package webpush sends browser notifications through the Web Push protocol.
//
// A parent who switches notifications on in the portal's settings gives the
// browser's push service (Google's for Chrome, Mozilla's for Firefox, Apple's
// for Safari) a subscription, which the portal registers with the backend.
// This package encrypts each message for that subscription and signs the
// request with the academy's VAPID key pair, so a push service accepts it from
// this server only. The keys come from the environment only; without them the
// channel stays off and browser deliveries are left pending.
package webpush

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	wp "github.com/SherClockHolmes/webpush-go"
)

// Config is read once at startup.
type Config struct {
	// PublicKey and PrivateKey are the VAPID pair, base64url, from
	// `go run ./cmd/vapidkeys`. The public half is also handed to browsers.
	PublicKey  string
	PrivateKey string
	// Subject is how a push service reaches whoever runs this server: an
	// email address or an https URL. Apple rejects a request without one.
	Subject string
}

func FromEnv() Config {
	return Config{
		PublicKey:  strings.TrimSpace(os.Getenv("WEBPUSH_VAPID_PUBLIC_KEY")),
		PrivateKey: strings.TrimSpace(os.Getenv("WEBPUSH_VAPID_PRIVATE_KEY")),
		Subject:    strings.TrimSpace(os.Getenv("WEBPUSH_SUBJECT")),
	}
}

// Configured reports whether browser push can be switched on: both keys and a
// subject are needed.
func (c Config) Configured() bool {
	return c.PublicKey != "" && c.PrivateKey != "" && c.Subject != ""
}

// Subscription is what a browser's PushManager hands over: where to send, and
// the two keys that encrypt a message so only that browser can read it.
type Subscription struct {
	Endpoint string
	P256dh   string
	Auth     string
}

// Result is what the push service said about one message.
type Result struct {
	OK bool
	// Gone means the subscription no longer reaches a browser (notifications
	// were turned off there, or the site data cleared), so it should not be
	// sent to again.
	Gone bool
	// Error is the push service's reason, for the delivery record. Never
	// shown to a user.
	Error string
}

// Client sends to browsers' push services.
type Client struct {
	cfg  Config
	http *http.Client
}

// New returns nil when the keys are not set, so callers can leave the channel
// off with a nil check.
func New(cfg Config) *Client {
	if !cfg.Configured() {
		return nil
	}
	return &Client{cfg: cfg, http: &http.Client{Timeout: 15 * time.Second}}
}

// PublicKey is the half a browser needs to subscribe; empty on a nil client.
func (c *Client) PublicKey() string {
	if c == nil {
		return ""
	}
	return c.cfg.PublicKey
}

// ttl is how long a push service holds a message for a browser that is
// offline. A check-in alert a day late is still worth seeing; one a week late
// is not, and the inbox has it anyway.
const ttl = 24 * 60 * 60

// Send encrypts payload for one browser and hands it to its push service.
func (c *Client) Send(ctx context.Context, sub Subscription, payload []byte) Result {
	resp, err := wp.SendNotificationWithContext(ctx, payload, &wp.Subscription{
		Endpoint: sub.Endpoint,
		Keys:     wp.Keys{P256dh: sub.P256dh, Auth: sub.Auth},
	}, &wp.Options{
		HTTPClient: c.http,
		// The library adds "mailto:" itself to anything that is not a URL.
		Subscriber:      strings.TrimPrefix(c.cfg.Subject, "mailto:"),
		VAPIDPublicKey:  c.cfg.PublicKey,
		VAPIDPrivateKey: c.cfg.PrivateKey,
		TTL:             ttl,
		Urgency:         wp.UrgencyNormal,
	})
	if err != nil {
		return Result{Error: err.Error()}
	}
	defer resp.Body.Close()
	reason, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return Result{OK: true}
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		return Result{Gone: true, Error: fmt.Sprintf("push service: %d, subscription gone", resp.StatusCode)}
	default:
		return Result{Error: fmt.Sprintf("push service: %d %s", resp.StatusCode, strings.TrimSpace(string(reason)))}
	}
}

// pushServices are the push services browsers subscribe through. The backend
// POSTs to whatever endpoint a subscription names, so an endpoint anywhere
// else — an address inside this server's network — is refused at
// registration rather than called.
var pushServices = []string{
	"fcm.googleapis.com",        // Chrome, Edge on Android, Opera, Brave
	"android.googleapis.com",    // Chrome's older endpoints
	"push.services.mozilla.com", // Firefox
	"push.apple.com",            // Safari (web.push.apple.com)
	"notify.windows.com",        // Edge on Windows
}

// AllowedEndpoint reports whether an endpoint is an https URL on a known push
// service.
func AllowedEndpoint(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	for _, s := range pushServices {
		if host == s || strings.HasSuffix(host, "."+s) {
			return true
		}
	}
	return false
}
