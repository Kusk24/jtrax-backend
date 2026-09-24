package api_test

/* A notification reaches a parent's phone: the app registers its Expo push
   token, and every notification the inbox gets is also pushed to it. */

import (
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type expoCalls struct {
	mu   sync.Mutex
	msgs []map[string]any
}

func (e *expoCalls) all() []map[string]any {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]map[string]any(nil), e.msgs...)
}

// fakeExpo answers every message ok, except tokens in `gone`, which it says
// are no longer registered.
func fakeExpo(t *testing.T, gone ...string) *expoCalls {
	t.Helper()
	calls := &expoCalls{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var msgs []map[string]any
		json.Unmarshal(raw, &msgs)
		tickets := []string{}
		calls.mu.Lock()
		for _, m := range msgs {
			calls.msgs = append(calls.msgs, m)
			ticket := `{"status":"ok","id":"x"}`
			for _, g := range gone {
				if m["to"] == g {
					ticket = `{"status":"error","message":"gone","details":{"error":"DeviceNotRegistered"}}`
				}
			}
			tickets = append(tickets, ticket)
		}
		calls.mu.Unlock()
		io.WriteString(w, `{"data":[`+strings.Join(tickets, ",")+`]}`)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("EXPO_PUSH_URL", srv.URL)
	return calls
}

func registerPhone(t *testing.T, c *client, token string) {
	t.Helper()
	if status, _, _ := c.do("POST", "/api/v1/push-subscriptions", map[string]any{
		"channel": "mobile", "endpoint": token, "user_agent": "ios",
	}); status != 201 {
		t.Fatalf("registering the phone: %d", status)
	}
}

func checkInPenny(t *testing.T, srv *httptest.Server) {
	t.Helper()
	teacher := &client{t: t, srv: srv}
	teacher.login("serene@jca.ac.th")
	if status, _, _ := teacher.do("POST", "/api/v1/attendance", map[string]any{
		"student_id": "stu_penny", "session_id": "ses_b3", "check_in_time": "2026-05-20T13:59:00",
	}); status != 201 {
		t.Fatalf("check-in: %d", status)
	}
}

func deliveryStatus(t *testing.T, d *sql.DB, typ string) string {
	t.Helper()
	var status string
	d.QueryRow(`SELECT nd.status FROM notification_delivery nd
	              JOIN notification n ON n.notification_id = nd.notification_id
	             WHERE n.type = ? AND nd.channel = 'mobile'
	             ORDER BY n.created_at DESC LIMIT 1`, typ).Scan(&status)
	return status
}

func TestACheckInIsPushedToTheParentsPhone(t *testing.T) {
	expo := fakeExpo(t)
	d := newDB(t)
	srv := newServerOn(t, d)
	sandy := &client{t: t, srv: srv}
	sandy.login("sandy01234@gmail.com")
	registerPhone(t, sandy, "ExponentPushToken[sandy-phone]")

	checkInPenny(t, srv)

	sent := expo.all()
	if len(sent) != 1 || sent[0]["to"] != "ExponentPushToken[sandy-phone]" {
		t.Fatalf("push sent: %v", sent)
	}
	data, _ := sent[0]["data"].(map[string]any)
	if data["type"] != "check_in" || data["studentId"] != "stu_penny" || data["notificationId"] == nil {
		t.Fatalf("the push should carry what the app needs to open the right screen: %v", data)
	}
	if got := deliveryStatus(t, d, "check_in"); got != "sent" {
		t.Fatalf("mobile delivery: %q, want sent", got)
	}
}

// A phone whose app was removed is dropped after Expo says so, and the next
// notification does not try it again.
func TestAPhoneThatIsGoneIsNotTriedAgain(t *testing.T) {
	expo := fakeExpo(t, "ExponentPushToken[old-phone]")
	d := newDB(t)
	srv := newServerOn(t, d)
	sandy := &client{t: t, srv: srv}
	sandy.login("sandy01234@gmail.com")
	registerPhone(t, sandy, "ExponentPushToken[old-phone]")

	checkInPenny(t, srv)
	if got := deliveryStatus(t, d, "check_in"); got != "failed" {
		t.Fatalf("mobile delivery to a removed app: %q, want failed", got)
	}
	var failedAt sql.NullString
	d.QueryRow(`SELECT failed_at FROM push_subscription WHERE endpoint = ?`, "ExponentPushToken[old-phone]").Scan(&failedAt)
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
	// It did go out — to the inbox — just not to the removed phone.
	if got := countType(inbox(t, srv, "sandy01234@gmail.com"), "announcement"); got != 1 {
		t.Fatalf("announcement inbox: %d", got)
	}
	if n := len(expo.all()); n != 1 {
		t.Fatalf("the removed phone was pushed to again: %d pushes", n)
	}
}

// Phone alerts follow the parent's own switch for that channel.
func TestAParentWhoTurnedPhoneAlertsOffGetsNone(t *testing.T) {
	expo := fakeExpo(t)
	d := newDB(t)
	srv := newServerOn(t, d)
	sandy := &client{t: t, srv: srv}
	sandy.login("sandy01234@gmail.com")
	registerPhone(t, sandy, "ExponentPushToken[sandy-phone]")
	if status, _, _ := sandy.do("PUT", "/api/v1/notification-settings", map[string]any{
		"type": "check_in", "channel": "mobile", "enabled": false,
	}); status != 200 {
		t.Fatalf("turning phone alerts off: %d", status)
	}

	checkInPenny(t, srv)
	if n := len(expo.all()); n != 0 {
		t.Fatalf("pushed despite the parent's switch: %d", n)
	}
	// The inbox still has it: only the phone channel was turned off.
	if got := countType(inbox(t, srv, "sandy01234@gmail.com"), "check_in"); got != 1 {
		t.Fatalf("inbox: %d", got)
	}
}
