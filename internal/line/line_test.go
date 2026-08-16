package line_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Kusk24/jtrax-backend/internal/line"
)

const secret = "0123456789abcdef0123456789abcdef"

func sign(body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func TestVerify(t *testing.T) {
	body := []byte(`{"destination":"Uabc","events":[]}`)
	good := sign(body)

	for _, tc := range []struct {
		name      string
		secret    string
		body      []byte
		signature string
		want      bool
	}{
		{"a genuine delivery", secret, body, good, true},
		{"a tampered body", secret, append(body, ' '), good, false},
		{"the wrong channel secret", "another-secret-entirely", body, good, false},
		{"no signature header", secret, body, "", false},
		{"signature that is not base64", secret, body, "!!!not base64!!!", false},
		// Before credentials are installed there is nothing to verify against,
		// and accepting everything would be the worst possible default.
		{"no channel secret configured", "", body, good, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := line.Verify(tc.secret, tc.body, tc.signature); got != tc.want {
				t.Errorf("Verify = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestParseEvents(t *testing.T) {
	body := []byte(`{"destination":"Uabc","events":[{
		"type":"message","replyToken":"rt","timestamp":1755300000000,
		"source":{"type":"user","userId":"U123"},
		"message":{"id":"m1","type":"text","text":"hello"}}]}`)

	events, err := line.ParseEvents(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("want 1 event, got %d", len(events))
	}
	e := events[0]
	if e.Type != "message" || e.Source.UserID != "U123" || e.Message.Text != "hello" {
		t.Errorf("parsed %+v", e)
	}
	// Milliseconds, read as UTC. Reading them as seconds would date every
	// message to 1970, and reading them as local time would shift a Bangkok
	// conversation by seven hours.
	if got := e.At().Format(time.RFC3339); got != "2025-08-15T23:20:00Z" {
		t.Errorf("At = %s, want 2025-08-15T23:20:00Z", got)
	}
}

// The LINE console's "Verify" button posts an empty batch. Answering it with an
// error makes the console report the webhook as broken.
func TestParseEventsAcceptsAnEmptyBatch(t *testing.T) {
	events, err := line.ParseEvents([]byte(`{"destination":"Uabc","events":[]}`))
	if err != nil {
		t.Fatalf("an empty batch is not an error: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("got %d events", len(events))
	}
}

// A failure has to arrive as something a receptionist can act on, and without
// the provider's own words attached.
func TestFailuresAreClassified(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   line.Reason
	}{
		{http.StatusTooManyRequests, line.ReasonQuota},
		{http.StatusForbidden, line.ReasonBlocked},
		{http.StatusBadRequest, line.ReasonInvalid},
		{http.StatusUnauthorized, line.ReasonInvalid},
		{http.StatusInternalServerError, line.ReasonNetwork},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(tc.status)
			w.Write([]byte(`{"message":"upstream detail that must not reach a browser"}`))
		}))
		c := line.New("token")
		c.BaseURL = srv.URL
		err := c.Push("U123", "hello")
		if err == nil {
			t.Errorf("status %d: want an error", tc.status)
		} else if got := line.ReasonOf(err); got != tc.want {
			t.Errorf("status %d: reason = %q, want %q", tc.status, got, tc.want)
		}
		srv.Close()
	}
}

func TestReasonOfAnUnrelatedErrorIsNetwork(t *testing.T) {
	c := line.New("token")
	c.BaseURL = "http://127.0.0.1:1" // nothing listening
	err := c.Push("U123", "hello")
	if err == nil {
		t.Fatal("want an error")
	}
	if got := line.ReasonOf(err); got != line.ReasonNetwork {
		t.Errorf("reason = %q, want network", got)
	}
}

func TestReplyAndPushHitTheRightEndpoints(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			t.Errorf("Authorization = %q", got)
		}
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c := line.New("tok")
	c.BaseURL = srv.URL
	if err := c.Reply("rt", "hi"); err != nil {
		t.Fatal(err)
	}
	if err := c.Push("U123", "hi"); err != nil {
		t.Fatal(err)
	}
	want := []string{"/v2/bot/message/reply", "/v2/bot/message/push"}
	for i := range want {
		if paths[i] != want[i] {
			t.Errorf("call %d went to %s, want %s", i, paths[i], want[i])
		}
	}
}

func TestQuotaReadsBothEndpoints(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/bot/message/quota":
			w.Write([]byte(`{"type":"limited","value":200}`))
		case "/v2/bot/message/quota/consumption":
			w.Write([]byte(`{"totalUsage":37}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	c := line.New("tok")
	c.BaseURL = srv.URL
	limit, used, limited, err := c.Quota()
	if err != nil {
		t.Fatal(err)
	}
	if limit != 200 || used != 37 || !limited {
		t.Errorf("Quota = (%d, %d, %v)", limit, used, limited)
	}
}
