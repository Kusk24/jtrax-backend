package api_test

/* A notification reaches a parent's browser: the portal registers the
   browser's push subscription, and every notification the inbox gets is also
   sent to it through the browser's push service, signed with the academy's
   VAPID key. */

import (
	"crypto/ecdh"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	wp "github.com/SherClockHolmes/webpush-go"
)

// withBrowserPush sets the VAPID keys the server reads at startup; call it
// before building the server.
func withBrowserPush(t *testing.T) string {
	t.Helper()
	priv, pub, err := wp.GenerateVAPIDKeys()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("WEBPUSH_VAPID_PUBLIC_KEY", pub)
	t.Setenv("WEBPUSH_VAPID_PRIVATE_KEY", priv)
	t.Setenv("WEBPUSH_SUBJECT", "office@jca.ac.th")
	return pub
}

// browserKeys are a subscription's p256dh and auth, as a browser gives them.
func browserKeys(t *testing.T) (string, string) {
	t.Helper()
	key, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	auth := make([]byte, 16)
	rand.Read(auth)
	return base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()), base64.RawURLEncoding.EncodeToString(auth)
}

// fakePushService stands in for a browser's push service, answering status.
func fakePushService(t *testing.T, status int) (*httptest.Server, func() int) {
	t.Helper()
	var mu sync.Mutex
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		if !strings.HasPrefix(r.Header.Get("Authorization"), "vapid t=") || r.Header.Get("Content-Encoding") != "aes128gcm" {
			t.Errorf("not a signed, encrypted push: %q %q", r.Header.Get("Authorization"), r.Header.Get("Content-Encoding"))
		}
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv, func() int { mu.Lock(); defer mu.Unlock(); return calls }
}

// subscribeBrowser stores Sandy's browser subscription directly: the endpoint
// is a local stand-in, which registration rightly refuses.
func subscribeBrowser(t *testing.T, d *sql.DB, endpoint string) {
	t.Helper()
	p256dh, auth := browserKeys(t)
	if _, err := d.Exec(`INSERT INTO push_subscription
	    (push_subscription_id, user_account_id, channel, endpoint, p256dh, auth)
	    VALUES ('psb_test', 'usr_sandy', 'webpush', ?, ?, ?)`, endpoint, p256dh, auth); err != nil {
		t.Fatal(err)
	}
}

func browserDelivery(t *testing.T, d *sql.DB, typ string) string {
	t.Helper()
	var status string
	d.QueryRow(`SELECT nd.status FROM notification_delivery nd
	              JOIN notification n ON n.notification_id = nd.notification_id
	             WHERE n.type = ? AND nd.channel = 'webpush'
	             ORDER BY n.created_at DESC LIMIT 1`, typ).Scan(&status)
	return status
}

func TestACheckInIsPushedToTheParentsBrowser(t *testing.T) {
	withBrowserPush(t)
	pushSvc, calls := fakePushService(t, http.StatusCreated)
	d := newDB(t)
	srv := newServerOn(t, d)
	subscribeBrowser(t, d, pushSvc.URL+"/fcm/send/sandy")

	checkInPenny(t, srv)

	if n := calls(); n != 1 {
		t.Fatalf("pushes to the browser: %d, want 1", n)
	}
	if got := browserDelivery(t, d, "check_in"); got != "sent" {
		t.Fatalf("browser delivery: %q, want sent", got)
	}
}

// A browser that turned notifications off is dropped once its push service
// says so, and is not tried again.
func TestABrowserThatIsGoneIsNotTriedAgain(t *testing.T) {
	withBrowserPush(t)
	pushSvc, calls := fakePushService(t, http.StatusGone)
	d := newDB(t)
	srv := newServerOn(t, d)
	subscribeBrowser(t, d, pushSvc.URL+"/fcm/send/old")

	checkInPenny(t, srv)
	if got := browserDelivery(t, d, "check_in"); got != "failed" {
		t.Fatalf("browser delivery to a dropped subscription: %q, want failed", got)
	}
	var failedAt sql.NullString
	d.QueryRow(`SELECT failed_at FROM push_subscription WHERE push_subscription_id = 'psb_test'`).Scan(&failedAt)
	if !failedAt.Valid {
		t.Fatal("the subscription should be marked failed")
	}

	admin := &client{t: t, srv: srv}
	admin.login("admin@jca.ac.th")
	if status, obj, _ := admin.do("POST", "/api/v1/announcements", map[string]any{
		"title": "Holiday", "body": "No class on Monday.", "author_user_account_id": "usr_admin1",
	}); status != 201 {
		t.Fatalf("announcement: %d %v", status, obj)
	}
	if n := calls(); n != 1 {
		t.Fatalf("the dropped browser was pushed to again: %d pushes", n)
	}
}

// Without keys the channel is off: the delivery waits, nothing is sent.
func TestWithoutKeysABrowserDeliveryWaits(t *testing.T) {
	pushSvc, calls := fakePushService(t, http.StatusCreated)
	d := newDB(t)
	srv := newServerOn(t, d)
	subscribeBrowser(t, d, pushSvc.URL+"/fcm/send/sandy")

	checkInPenny(t, srv)
	if n := calls(); n != 0 {
		t.Fatalf("pushed without keys: %d", n)
	}
	if got := browserDelivery(t, d, "check_in"); got != "pending" {
		t.Fatalf("browser delivery: %q, want pending", got)
	}
}

func TestOnlyAPushServiceSubscriptionIsAcceptedFromABrowser(t *testing.T) {
	srv := newServer(t)
	sandy := &client{t: t, srv: srv}
	sandy.login("sandy01234@gmail.com")
	p256dh, auth := browserKeys(t)
	fcm := "https://fcm.googleapis.com/fcm/send/abc:def"

	bad := []map[string]any{
		{"channel": "webpush", "endpoint": "http://127.0.0.1:8790/api/v1/users", "p256dh": p256dh, "auth": auth},
		{"channel": "webpush", "endpoint": "https://evil.example/hook", "p256dh": p256dh, "auth": auth},
		{"channel": "webpush", "endpoint": fcm},
		{"channel": "webpush", "endpoint": fcm, "p256dh": "short", "auth": auth},
		{"channel": "webpush", "endpoint": fcm, "p256dh": p256dh, "auth": "!!"},
	}
	for _, body := range bad {
		if status, _, _ := sandy.do("POST", "/api/v1/push-subscriptions", body); status != 400 {
			t.Errorf("%.60v: want 400, got %d", body, status)
		}
	}
	if status, _, _ := sandy.do("POST", "/api/v1/push-subscriptions", map[string]any{
		"channel": "webpush", "endpoint": fcm, "p256dh": p256dh, "auth": auth, "user_agent": "Chrome",
	}); status != 201 {
		t.Fatalf("a real browser subscription: want 201, got %d", status)
	}
}

func TestTheBrowserKeyGoesOnlyToSignedInCallers(t *testing.T) {
	// Not set up: the portal is told so, and leaves the switch out.
	srv := newServer(t)
	sandy := &client{t: t, srv: srv}
	sandy.login("sandy01234@gmail.com")
	if status, _, _ := sandy.do("GET", "/api/v1/push-subscriptions/webpush-key", nil); status != 404 {
		t.Fatalf("without keys: want 404, got %d", status)
	}

	pub := withBrowserPush(t)
	srv = newServer(t)
	sandy = &client{t: t, srv: srv}
	sandy.login("sandy01234@gmail.com")
	status, obj, _ := sandy.do("GET", "/api/v1/push-subscriptions/webpush-key", nil)
	if status != 200 || obj["publicKey"] != pub {
		t.Fatalf("with keys: %d %v", status, obj)
	}
	anon := &client{t: t, srv: srv}
	if status, _, _ := anon.do("GET", "/api/v1/push-subscriptions/webpush-key", nil); status != 401 {
		t.Fatalf("anonymous: want 401, got %d", status)
	}
}
