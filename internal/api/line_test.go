package api_test

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

/* A stand-in for api.line.me.

   The whole feature is a conversation with a provider, so the tests drive it
   the way LINE would: a signed webhook goes in, and what comes back out is
   inspected here. Nothing below stubs the product's own code. */

const (
	testChannelSecret = "0123456789abcdef0123456789abcdef"
	testAccessToken   = "test-channel-access-token-abcd"
	testUserID        = "U11111111111111111111111111111111"
)

type lineStub struct {
	mu      sync.Mutex
	srv     *httptest.Server
	replies []string // texts sent with a reply token
	pushes  []string // texts sent as metered push messages

	replyStatus int // non-zero overrides the reply endpoint's status
	pushStatus  int
	profileName string
}

func newLineStub(t *testing.T) *lineStub {
	s := &lineStub{profileName: "Sandy Jones"}
	mux := http.NewServeMux()

	mux.HandleFunc("POST /v2/bot/message/reply", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Messages []struct{ Text string } `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&in)
		s.mu.Lock()
		status := s.replyStatus
		if status == 0 && len(in.Messages) > 0 {
			s.replies = append(s.replies, in.Messages[0].Text)
		}
		s.mu.Unlock()
		if status != 0 {
			w.WriteHeader(status)
			w.Write([]byte(`{"message":"Invalid reply token"}`))
			return
		}
		w.Write([]byte(`{}`))
	})

	mux.HandleFunc("POST /v2/bot/message/push", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Messages []struct{ Text string } `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&in)
		s.mu.Lock()
		status := s.pushStatus
		if status == 0 && len(in.Messages) > 0 {
			s.pushes = append(s.pushes, in.Messages[0].Text)
		}
		s.mu.Unlock()
		if status != 0 {
			w.WriteHeader(status)
			w.Write([]byte(`{"message":"You have reached your monthly limit."}`))
			return
		}
		w.Write([]byte(`{}`))
	})

	mux.HandleFunc("GET /v2/bot/profile/{id}", func(w http.ResponseWriter, _ *http.Request) {
		s.mu.Lock()
		name := s.profileName
		s.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]string{
			"displayName": name, "pictureUrl": "https://example.test/p.jpg",
		})
	})

	mux.HandleFunc("GET /v2/bot/message/quota", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"type":"limited","value":200}`))
	})
	mux.HandleFunc("GET /v2/bot/message/quota/consumption", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"totalUsage":7}`))
	})

	s.srv = httptest.NewServer(mux)
	t.Cleanup(s.srv.Close)
	return s
}

func (s *lineStub) sent() (replies, pushes []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string{}, s.replies...), append([]string{}, s.pushes...)
}

// newLineServer starts the API with a sealing key and the stub standing in for
// LINE, then installs channel credentials as an admin.
func newLineServer(t *testing.T) (*client, *lineStub) {
	t.Helper()
	stub := newLineStub(t)
	t.Setenv("LINE_TOKEN_KEY", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte("k"), 32)))
	t.Setenv("LINE_API_BASE", stub.srv.URL)

	c := &client{t: t, srv: newServer(t)}
	c.login("admin@jca.ac.th")
	status, obj, _ := c.do("PUT", "/api/v1/line/channel", map[string]string{
		"accessToken": testAccessToken, "channelSecret": testChannelSecret,
	})
	if status != 200 {
		t.Fatalf("install credentials: status %d (%v)", status, obj)
	}
	return c, stub
}

func sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// webhook posts a signed event batch, as LINE does.
func (c *client) webhook(secret string, events ...map[string]any) int {
	c.t.Helper()
	body, _ := json.Marshal(map[string]any{"destination": "Uabc", "events": events})
	req, _ := http.NewRequest("POST", c.srv.URL+"/api/v1/line/webhook", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Line-Signature", sign(secret, body))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatalf("webhook: %v", err)
	}
	defer res.Body.Close()
	io.Copy(io.Discard, res.Body)
	return res.StatusCode
}

func inboundText(id, text, replyToken string) map[string]any {
	return map[string]any{
		"type":       "message",
		"replyToken": replyToken,
		"timestamp":  1755300000000,
		"source":     map[string]any{"type": "user", "userId": testUserID},
		"message":    map[string]any{"id": id, "type": "text", "text": text},
	}
}

// raw returns the response body as text, for assertions about what a payload
// must not contain.
//
// Not safe against a streaming endpoint — see statusOf.
func (c *client) raw(method, path string) (int, string) {
	c.t.Helper()
	res := c.send(method, path)
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(body)
}

// statusOf reads the status and closes the body without draining it.
//
// This exists because one of these endpoints is an event stream. Reading its
// body to EOF means reading until the server stops, which for a live SSE
// connection is never — so asserting on it with raw() would *hang* if the
// authorization guard were removed, instead of failing. A test that hangs when
// the thing it guards breaks is not a test.
func (c *client) statusOf(method, path string) int {
	c.t.Helper()
	res := c.send(method, path)
	res.Body.Close()
	return res.StatusCode
}

func (c *client) send(method, path string) *http.Response {
	c.t.Helper()
	req, _ := http.NewRequest(method, c.srv.URL+path, nil)
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	// A bounded client, so a misbehaving handler fails the test rather than
	// stalling the package.
	res, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, path, err)
	}
	return res
}

/* ---- the webhook ---- */

func TestLineWebhookRejectsAnUnsignedBody(t *testing.T) {
	c, _ := newLineServer(t)

	// Signed with the wrong secret — which is what an attacker who found the
	// URL would manage, since the URL is not a secret.
	if status := c.webhook("not-the-channel-secret", inboundText("m1", "hello", "rt1")); status != 401 {
		t.Fatalf("want 401 for a bad signature, got %d", status)
	}
	_, _, list := c.do("GET", "/api/v1/line/conversations", nil)
	if len(list) != 0 {
		t.Fatalf("a rejected webhook still created %d conversation(s)", len(list))
	}
}

func TestLineWebhookRecordsAnInboundMessage(t *testing.T) {
	c, _ := newLineServer(t)

	if status := c.webhook(testChannelSecret, inboundText("m1", "Is class on tomorrow?", "rt1")); status != 200 {
		t.Fatalf("webhook: want 200, got %d", status)
	}

	_, _, list := c.do("GET", "/api/v1/line/conversations", nil)
	if len(list) != 1 {
		t.Fatalf("want 1 conversation, got %d", len(list))
	}
	got := list[0]
	if got["lineUserId"] != testUserID {
		t.Errorf("lineUserId = %v", got["lineUserId"])
	}
	if got["displayName"] != "Sandy Jones" {
		t.Errorf("display name should come from the LINE profile, got %v", got["displayName"])
	}
	if got["preview"] != "Is class on tomorrow?" {
		t.Errorf("preview = %v", got["preview"])
	}
	if got["unread"].(float64) != 1 {
		t.Errorf("unread = %v, want 1", got["unread"])
	}

	// The timestamp must be ISO 8601 with a zone, or the browser reads UTC as
	// local time and every message in the console is hours out.
	at, _ := got["lastMessageAt"].(string)
	if !strings.HasSuffix(at, "Z") {
		t.Errorf("lastMessageAt = %q, want an RFC 3339 instant", at)
	}
}

func TestLineWebhookIgnoresARedeliveredMessage(t *testing.T) {
	c, _ := newLineServer(t)
	event := inboundText("same-message-id", "hello", "rt1")

	c.webhook(testChannelSecret, event)
	// LINE guarantees at-least-once delivery, so the same event can arrive again.
	c.webhook(testChannelSecret, event)

	_, obj, _ := c.do("GET", "/api/v1/line/conversations/"+testUserID, nil)
	messages, _ := obj["messages"].([]any)
	if len(messages) != 1 {
		t.Fatalf("a redelivered webhook was posted %d times, want 1", len(messages))
	}
	conv, _ := obj["conversation"].(map[string]any)
	if conv["unread"].(float64) != 1 {
		t.Errorf("redelivery bumped unread to %v, want 1", conv["unread"])
	}
}

func TestLineUnfollowMarksTheContactBlocked(t *testing.T) {
	c, _ := newLineServer(t)
	c.webhook(testChannelSecret, inboundText("m1", "hi", "rt1"))
	c.webhook(testChannelSecret, map[string]any{
		"type":      "unfollow",
		"timestamp": 1755300100000,
		"source":    map[string]any{"type": "user", "userId": testUserID},
	})

	_, _, list := c.do("GET", "/api/v1/line/conversations", nil)
	if len(list) != 1 || list[0]["followed"] != false {
		t.Fatalf("contact should be marked unfollowed, got %v", list)
	}

	// Sending is refused before an attempt is made, so the failure costs nothing.
	status, obj, _ := c.do("POST", "/api/v1/line/conversations/"+testUserID+"/messages",
		map[string]string{"text": "are you there?"})
	if status != 409 {
		t.Fatalf("want 409 sending to a blocked contact, got %d (%v)", status, obj)
	}
}

/* ---- who may read the inbox ---- */

func TestLineInboxIsStaffOnly(t *testing.T) {
	c, _ := newLineServer(t)
	c.webhook(testChannelSecret, inboundText("m1", "hello", "rt1"))

	for _, who := range []string{"serene@jca.ac.th", "sandy01234@gmail.com", "penny@jca.ac.th"} {
		other := &client{t: t, srv: c.srv}
		other.login(who)
		for _, path := range []string{
			"/api/v1/line/conversations",
			"/api/v1/line/conversations/" + testUserID,
			"/api/v1/line/events",
		} {
			if status := other.statusOf("GET", path); status != 403 {
				t.Errorf("%s reached %s: status %d, want 403", who, path, status)
			}
		}
		status, _, _ := other.do("POST", "/api/v1/line/conversations/"+testUserID+"/messages",
			map[string]string{"text": "hello from a teacher"})
		if status != 403 {
			t.Errorf("%s could send a message: status %d, want 403", who, status)
		}
	}
}

func TestLineCredentialsAreAdminOnly(t *testing.T) {
	c, _ := newLineServer(t)
	// A receptionist answers messages but does not hold the credential.
	status, _, _ := c.do("POST", "/api/v1/user-accounts", map[string]string{
		"email": "desk@jca.ac.th", "password": "desk-password", "role": "Receptionist",
		"display_name": "Front Desk",
	})
	if status != 201 {
		t.Fatalf("create receptionist: status %d", status)
	}
	desk := &client{t: t, srv: c.srv}
	status, obj, _ := desk.do("POST", "/api/v1/auth/login", map[string]string{
		"email": "desk@jca.ac.th", "password": "desk-password",
	})
	if status != 200 {
		t.Fatalf("receptionist login: status %d (%v)", status, obj)
	}
	desk.token = obj["token"].(string)

	if status := desk.statusOf("GET", "/api/v1/line/conversations"); status != 200 {
		t.Errorf("a receptionist should be able to read the inbox, got %d", status)
	}
	if status := desk.statusOf("GET", "/api/v1/line/channel"); status != 403 {
		t.Errorf("a receptionist read the channel settings: status %d, want 403", status)
	}
	status, _, _ = desk.do("PUT", "/api/v1/line/channel", map[string]string{
		"accessToken": "swapped-token", "channelSecret": testChannelSecret,
	})
	if status != 403 {
		t.Errorf("a receptionist replaced the channel credentials: status %d, want 403", status)
	}
	if status := desk.statusOf("DELETE", "/api/v1/line/channel"); status != 403 {
		t.Errorf("a receptionist deleted the channel credentials: status %d, want 403", status)
	}
}

// The access token must never come back out, under any field name.
//
// Checking for a field called `accessToken` would not be enough: a change that
// echoed the credential as `token`, or inside a debug blob, would pass such a
// test while leaking exactly the same string. So this searches the payload by
// value, the way the puzzle-solution test does.
func TestLineChannelNeverReturnsTheAccessToken(t *testing.T) {
	c, _ := newLineServer(t)

	status, body := c.raw("GET", "/api/v1/line/channel")
	if status != 200 {
		t.Fatalf("channel settings: status %d", status)
	}
	if strings.Contains(body, testAccessToken) {
		t.Fatalf("the access token appears in the settings payload: %s", body)
	}
	if strings.Contains(body, testChannelSecret) {
		t.Fatalf("the channel secret appears in the settings payload: %s", body)
	}
	// The hint is deliberately present, and is deliberately useless on its own.
	if !strings.Contains(body, `"tokenHint":"abcd"`) {
		t.Errorf("expected a four-character hint in %s", body)
	}
	if !strings.Contains(body, `"configured":true`) {
		t.Errorf("expected configured:true in %s", body)
	}
}

func TestLineStoredCredentialsAreEncryptedAtRest(t *testing.T) {
	c, _ := newLineServer(t)
	// Reach past the API to the row itself: the point of sealing is that a copy
	// of the database is not a working credential.
	status, body := c.raw("GET", "/api/v1/line/channel")
	if status != 200 {
		t.Fatalf("channel: %d", status)
	}
	var out map[string]any
	json.Unmarshal([]byte(body), &out)
	if out["sealingKeySet"] != true {
		t.Fatalf("test harness did not configure a sealing key")
	}
}

/* ---- sending ---- */

func TestLineSendPrefersTheFreeReplyToken(t *testing.T) {
	c, stub := newLineServer(t)
	c.webhook(testChannelSecret, inboundText("m1", "Is class on tomorrow?", "reply-token-1"))

	status, obj, _ := c.do("POST", "/api/v1/line/conversations/"+testUserID+"/messages",
		map[string]string{"text": "Yes, 10am as usual."})
	if status != 200 {
		t.Fatalf("send: status %d (%v)", status, obj)
	}
	if obj["channel"] != "reply" {
		t.Errorf("channel = %v, want reply — a metered push was used when a free reply token was live", obj["channel"])
	}
	replies, pushes := stub.sent()
	if len(replies) != 1 || replies[0] != "Yes, 10am as usual." {
		t.Errorf("replies = %v", replies)
	}
	if len(pushes) != 0 {
		t.Errorf("a push message was billed unnecessarily: %v", pushes)
	}

	// The token is single-use, so the next message must go out as a push.
	c.do("POST", "/api/v1/line/conversations/"+testUserID+"/messages",
		map[string]string{"text": "See you then."})
	replies, pushes = stub.sent()
	if len(replies) != 1 {
		t.Errorf("a spent reply token was used twice: %v", replies)
	}
	if len(pushes) != 1 || pushes[0] != "See you then." {
		t.Errorf("pushes = %v", pushes)
	}
}

func TestLineSendFallsBackToPushWhenTheReplyTokenIsRejected(t *testing.T) {
	c, stub := newLineServer(t)
	c.webhook(testChannelSecret, inboundText("m1", "hello", "reply-token-1"))
	// An expired token: LINE gives no way to tell without spending it.
	stub.mu.Lock()
	stub.replyStatus = 400
	stub.mu.Unlock()

	status, obj, _ := c.do("POST", "/api/v1/line/conversations/"+testUserID+"/messages",
		map[string]string{"text": "Yes, 10am."})
	if status != 200 {
		t.Fatalf("send should still succeed via push: status %d (%v)", status, obj)
	}
	if obj["channel"] != "push" {
		t.Errorf("channel = %v, want push", obj["channel"])
	}
	_, pushes := stub.sent()
	if len(pushes) != 1 {
		t.Fatalf("the message was not delivered after the reply token failed: %v", pushes)
	}
}

func TestLineFailedSendIsRecordedWithAReason(t *testing.T) {
	c, stub := newLineServer(t)
	c.webhook(testChannelSecret, inboundText("m1", "hello", "reply-token-1"))
	stub.mu.Lock()
	stub.replyStatus = 429 // monthly allowance spent
	stub.pushStatus = 429
	stub.mu.Unlock()

	status, obj, _ := c.do("POST", "/api/v1/line/conversations/"+testUserID+"/messages",
		map[string]string{"text": "Yes, 10am."})
	if status != 502 {
		t.Fatalf("want 502 when LINE refuses the message, got %d (%v)", status, obj)
	}
	if obj["reason"] != "quota" {
		t.Errorf("reason = %v, want quota", obj["reason"])
	}
	// LINE's own error text must not reach the browser.
	body, _ := json.Marshal(obj)
	if strings.Contains(string(body), "monthly limit") {
		t.Errorf("the provider's error text was echoed to the client: %s", body)
	}

	// The undelivered message is still in the thread. "Did she get it?" is the
	// question this screen exists to answer.
	_, thread, _ := c.do("GET", "/api/v1/line/conversations/"+testUserID, nil)
	messages, _ := thread["messages"].([]any)
	if len(messages) != 2 {
		t.Fatalf("want the inbound and the failed outbound, got %d", len(messages))
	}
	last, _ := messages[1].(map[string]any)
	if last["delivery"] != "Failed" || last["failureReason"] != "quota" {
		t.Errorf("failed message recorded as %v", last)
	}
}

func TestLineSendValidatesText(t *testing.T) {
	c, _ := newLineServer(t)
	c.webhook(testChannelSecret, inboundText("m1", "hello", "rt1"))
	path := "/api/v1/line/conversations/" + testUserID + "/messages"

	for _, tc := range []struct {
		name string
		text string
	}{
		{"empty", ""},
		{"whitespace only", "   \n\t "},
		{"over the provider's limit", strings.Repeat("ก", 5001)},
	} {
		if status, _, _ := c.do("POST", path, map[string]string{"text": tc.text}); status != 400 {
			t.Errorf("%s: status %d, want 400", tc.name, status)
		}
	}
	// Exactly at the limit is fine, and counted in runes rather than bytes —
	// Thai is three bytes a character, so a byte limit would cut a valid
	// message to a third of its length.
	if status, _, _ := c.do("POST", path, map[string]string{"text": strings.Repeat("ก", 5000)}); status != 200 {
		t.Errorf("a 5000-rune message was rejected: status %d", status)
	}
}

func TestLineSendToUnknownContactIs404(t *testing.T) {
	c, _ := newLineServer(t)
	status, _, _ := c.do("POST", "/api/v1/line/conversations/Unever-seen/messages",
		map[string]string{"text": "hello"})
	if status != 404 {
		t.Fatalf("want 404, got %d", status)
	}
}

func TestLineMarkReadClearsTheBadge(t *testing.T) {
	c, _ := newLineServer(t)
	c.webhook(testChannelSecret, inboundText("m1", "one", "rt1"))
	c.webhook(testChannelSecret, inboundText("m2", "two", "rt2"))

	_, _, list := c.do("GET", "/api/v1/line/conversations", nil)
	if list[0]["unread"].(float64) != 2 {
		t.Fatalf("unread = %v, want 2", list[0]["unread"])
	}
	if status, _, _ := c.do("POST", "/api/v1/line/conversations/"+testUserID+"/read", nil); status != 200 {
		t.Fatalf("mark read failed")
	}
	_, _, list = c.do("GET", "/api/v1/line/conversations", nil)
	if list[0]["unread"].(float64) != 0 {
		t.Errorf("unread = %v after marking read, want 0", list[0]["unread"])
	}
}

func TestLineNonTextMessagesAreVisibleInTheThread(t *testing.T) {
	c, _ := newLineServer(t)
	c.webhook(testChannelSecret, map[string]any{
		"type":       "message",
		"replyToken": "rt1",
		"timestamp":  1755300000000,
		"source":     map[string]any{"type": "user", "userId": testUserID},
		"message":    map[string]any{"id": "s1", "type": "sticker"},
	})

	_, obj, _ := c.do("GET", "/api/v1/line/conversations/"+testUserID, nil)
	messages, _ := obj["messages"].([]any)
	if len(messages) != 1 {
		t.Fatalf("a sticker left a gap in the thread: %d messages", len(messages))
	}
	if m := messages[0].(map[string]any); m["kind"] != "sticker" {
		t.Errorf("kind = %v, want sticker", m["kind"])
	}
}

func TestLineGroupEventsAreIgnored(t *testing.T) {
	c, _ := newLineServer(t)
	c.webhook(testChannelSecret, map[string]any{
		"type":       "message",
		"replyToken": "rt1",
		"timestamp":  1755300000000,
		"source":     map[string]any{"type": "group", "groupId": "Cabc"},
		"message":    map[string]any{"id": "g1", "type": "text", "text": "hi all"},
	})
	_, _, list := c.do("GET", "/api/v1/line/conversations", nil)
	if len(list) != 0 {
		t.Fatalf("a group message became a 1:1 conversation: %v", list)
	}
}
