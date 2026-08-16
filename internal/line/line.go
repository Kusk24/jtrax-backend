// Package line talks to the LINE Messaging API: it verifies the signature on
// an inbound webhook, parses the events, and sends messages back.
//
// Plain net/http rather than the official SDK, for the same reason mail speaks
// SMTP rather than a provider SDK — this is four endpoints and a HMAC, and the
// SDK would be the first dependency in the tree with its own transitive graph.
// CGO_ENABLED=0 and the distroless image stay as they are.
//
// # The two ways to send, and why the difference matters
//
// A reply uses the token attached to an inbound event. It is free, unmetered,
// and expires shortly after the message arrives. A push names the recipient
// directly, works at any time, and is billed against the Official Account's
// monthly message allowance.
//
// A school answering a parent an hour later is therefore paying for something
// that would have been free at the time, and on the free plan the allowance is
// small. Send prefers a live reply token and falls back to push, and records
// which it used so the console can show where the allowance went.
package line

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// DefaultBaseURL is LINE's API host. Overridden in tests.
const DefaultBaseURL = "https://api.line.me"

// MaxTextLength is LINE's limit for one text message. Longer bodies are
// rejected at the boundary rather than sent and truncated by the provider.
const MaxTextLength = 5000

// ReplyTokenTTL is how long a reply token is treated as usable.
//
// LINE documents a short life without promising a number, so this is
// deliberately conservative: spending a token that has just expired costs a
// failed call and a retry, while treating a live one as dead costs a metered
// push message. The failure is recoverable and the waste is not, so Send tries
// the reply first and falls back when LINE rejects it.
const ReplyTokenTTL = 50 * time.Second

// Verify reports whether a webhook body carries a valid signature.
//
// The comparison is constant-time and the body must be the exact bytes
// received — re-encoding parsed JSON changes the digest, so callers read the
// raw body first and parse afterwards.
func Verify(channelSecret string, body []byte, signature string) bool {
	if channelSecret == "" || signature == "" {
		return false
	}
	mac := hmac.New(sha256.New, []byte(channelSecret))
	mac.Write(body)
	want := mac.Sum(nil)
	got, err := base64.StdEncoding.DecodeString(signature)
	if err != nil {
		return false
	}
	return hmac.Equal(want, got)
}

// Event is the subset of a webhook event this product uses.
type Event struct {
	Type       string `json:"type"` // message | follow | unfollow | …
	ReplyToken string `json:"replyToken"`
	Timestamp  int64  `json:"timestamp"` // milliseconds since the epoch
	Source     struct {
		Type   string `json:"type"` // user | group | room
		UserID string `json:"userId"`
	} `json:"source"`
	Message struct {
		ID   string `json:"id"`
		Type string `json:"type"` // text | sticker | image | …
		Text string `json:"text"`
	} `json:"message"`
}

// At is the event time. LINE sends milliseconds; a zero timestamp falls back to
// now so a malformed event still sorts sensibly in a thread.
func (e Event) At() time.Time {
	if e.Timestamp == 0 {
		return time.Now().UTC()
	}
	return time.UnixMilli(e.Timestamp).UTC()
}

// ParseEvents reads the webhook envelope.
//
// An empty list is not an error: LINE's console sends exactly that when an
// operator clicks Verify, and answering it with a 400 makes the console report
// the webhook as broken.
func ParseEvents(body []byte) ([]Event, error) {
	var envelope struct {
		Events []Event `json:"events"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("parse webhook: %w", err)
	}
	return envelope.Events, nil
}

// Client sends to one channel.
type Client struct {
	Token   string
	BaseURL string
	HTTP    *http.Client
}

// New builds a client with a bounded timeout. Without one a hung LINE
// connection would hold the request goroutine — and the composer's spinner —
// indefinitely.
func New(token string) *Client {
	return &Client{Token: token, BaseURL: DefaultBaseURL, HTTP: &http.Client{Timeout: 10 * time.Second}}
}

// Reason classifies a failure into something a receptionist can act on. The
// provider's own error text is never carried out of this package: it goes to
// the log, and the caller gets one of these instead.
type Reason string

const (
	ReasonQuota   Reason = "quota"   // the monthly allowance is spent
	ReasonBlocked Reason = "blocked" // the recipient blocked the account
	ReasonInvalid Reason = "invalid" // bad token, bad recipient, expired reply token
	ReasonNetwork Reason = "network" // LINE unreachable or erroring
)

// Error is a failed call, carrying a classification and the detail for logs.
type Error struct {
	Reason Reason
	Status int
	detail string
}

func (e *Error) Error() string {
	return fmt.Sprintf("line: %s (status %d): %s", e.Reason, e.Status, e.detail)
}

// Reply answers an inbound message using its token. Free and unmetered.
func (c *Client) Reply(replyToken, text string) error {
	return c.post("/v2/bot/message/reply", map[string]any{
		"replyToken": replyToken,
		"messages":   []any{map[string]string{"type": "text", "text": text}},
	})
}

// Push sends to a user at any time. Counts against the monthly allowance.
func (c *Client) Push(userID, text string) error {
	return c.post("/v2/bot/message/push", map[string]any{
		"to":       userID,
		"messages": []any{map[string]string{"type": "text", "text": text}},
	})
}

// Profile fetches a contact's display name and picture.
//
// Only available for people who have added the account as a friend, so a
// failure here is normal and must never block recording their message — the
// caller keeps whatever name it already had.
func (c *Client) Profile(userID string) (name, picture string, err error) {
	req, err := http.NewRequest(http.MethodGet, c.base()+"/v2/bot/profile/"+userID, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	res, err := c.do(req)
	if err != nil {
		return "", "", err
	}
	defer res.Body.Close()
	if err := classify(res); err != nil {
		return "", "", err
	}
	var out struct {
		DisplayName string `json:"displayName"`
		PictureURL  string `json:"pictureUrl"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return "", "", err
	}
	return out.DisplayName, out.PictureURL, nil
}

// Quota reports the monthly allowance and how much of it is spent.
//
// Surfaced in the console because on the free plan this number is the real
// constraint on the feature, and finding out by having a message silently fail
// is the worst way to learn it. A plan with no cap reports limited=false.
func (c *Client) Quota() (limit int64, used int64, limited bool, err error) {
	var q struct {
		Type  string `json:"type"` // "none" | "limited"
		Value int64  `json:"value"`
	}
	if err := c.get("/v2/bot/message/quota", &q); err != nil {
		return 0, 0, false, err
	}
	var consumed struct {
		TotalUsage int64 `json:"totalUsage"`
	}
	if err := c.get("/v2/bot/message/quota/consumption", &consumed); err != nil {
		return 0, 0, false, err
	}
	return q.Value, consumed.TotalUsage, q.Type == "limited", nil
}

func (c *Client) base() string {
	if c.BaseURL == "" {
		return DefaultBaseURL
	}
	return c.BaseURL
}

func (c *Client) do(req *http.Request) (*http.Response, error) {
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, &Error{Reason: ReasonNetwork, detail: err.Error()}
	}
	return res, nil
}

func (c *Client) get(path string, dst any) error {
	req, err := http.NewRequest(http.MethodGet, c.base()+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	res, err := c.do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if err := classify(res); err != nil {
		return err
	}
	return json.NewDecoder(res.Body).Decode(dst)
}

func (c *Client) post(path string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, c.base()+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	res, err := c.do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	return classify(res)
}

// classify turns an HTTP response into nil or a classified Error.
func classify(res *http.Response) error {
	if res.StatusCode >= 200 && res.StatusCode < 300 {
		return nil
	}
	// Bounded read: an error body is a short JSON object, and this one is only
	// ever written to the log.
	detail, _ := io.ReadAll(io.LimitReader(res.Body, 2048))
	reason := ReasonNetwork
	switch {
	case res.StatusCode == http.StatusTooManyRequests:
		// LINE returns 429 both for the monthly allowance being spent and for
		// short-term rate limiting. Neither is retryable from a composer, and
		// "out of messages" is the one a school actually hits.
		reason = ReasonQuota
	case res.StatusCode == http.StatusForbidden:
		reason = ReasonBlocked
	case res.StatusCode >= 400 && res.StatusCode < 500:
		reason = ReasonInvalid
	}
	return &Error{Reason: reason, Status: res.StatusCode, detail: string(detail)}
}

// ReasonOf extracts the classification from an error, defaulting to network for
// anything that did not come from here.
func ReasonOf(err error) Reason {
	var e *Error
	if errors.As(err, &e) {
		return e.Reason
	}
	return ReasonNetwork
}
