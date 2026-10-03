// Package push sends phone notifications through Expo's push service.
//
// The phone app registers an Expo push token (ExponentPushToken[…]) with the
// backend; this package hands messages for those tokens to Expo, which passes
// them on to Apple and Google. Expo's service is free and needs no key. An
// access token is optional ("enhanced push security" in the Expo dashboard)
// and, when set, comes from the environment only.
package push

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const expoURL = "https://exp.host/--/api/v2/push/send"

// Expo takes at most 100 messages in one request.
const batchSize = 100

// Config is read once at startup.
type Config struct {
	// AccessToken is only needed when the Expo project requires it.
	AccessToken string
	// URL overrides Expo's endpoint, for tests and a local stub.
	URL string
}

func FromEnv() Config {
	return Config{
		AccessToken: strings.TrimSpace(os.Getenv("EXPO_ACCESS_TOKEN")),
		URL:         strings.TrimSpace(os.Getenv("EXPO_PUSH_URL")),
	}
}

// Message is one notification for one phone.
type Message struct {
	To    string
	Title string
	Body  string
	// Data travels with the notification, for the app to open the right screen.
	Data map[string]any
}

// Result is what Expo said about one message, in the order they were sent.
type Result struct {
	OK bool
	// Unregistered means the token no longer reaches a phone (the app was
	// removed, or notifications revoked), so it should not be sent to again.
	Unregistered bool
	// Error is Expo's reason, for the delivery record. Never shown to a user.
	Error string
}

// Client sends to Expo.
type Client struct {
	cfg  Config
	http *http.Client
}

func New(cfg Config) *Client {
	return &Client{cfg: cfg, http: &http.Client{Timeout: 15 * time.Second}}
}

// IsExpoToken reports whether a registered endpoint is an Expo push token, the
// only kind this package can deliver to.
func IsExpoToken(token string) bool {
	return strings.HasPrefix(token, "ExponentPushToken[") || strings.HasPrefix(token, "ExpoPushToken[")
}

type expoMessage struct {
	To        string         `json:"to"`
	Title     string         `json:"title"`
	Body      string         `json:"body"`
	Data      map[string]any `json:"data,omitempty"`
	Sound     string         `json:"sound"`
	ChannelID string         `json:"channelId"`
	Priority  string         `json:"priority"`
}

type expoTicket struct {
	Status  string `json:"status"`
	Message string `json:"message"`
	Details struct {
		Error string `json:"error"`
	} `json:"details"`
}

// Send delivers the messages, in batches, and returns one Result per message.
// An error means a whole batch could not be handed to Expo; those messages come
// back as failed results rather than stopping the rest.
func (c *Client) Send(ctx context.Context, msgs []Message) []Result {
	out := make([]Result, 0, len(msgs))
	for start := 0; start < len(msgs); start += batchSize {
		end := min(start+batchSize, len(msgs))
		out = append(out, c.sendBatch(ctx, msgs[start:end])...)
	}
	return out
}

func (c *Client) sendBatch(ctx context.Context, msgs []Message) []Result {
	fail := func(reason string) []Result {
		r := make([]Result, len(msgs))
		for i := range r {
			r[i] = Result{Error: reason}
		}
		return r
	}

	payload := make([]expoMessage, len(msgs))
	for i, m := range msgs {
		payload[i] = expoMessage{
			To: m.To, Title: m.Title, Body: m.Body, Data: m.Data,
			// The app creates a channel called "default"; Android shows a
			// notification with no channel only if one exists.
			Sound: "default", ChannelID: "default", Priority: "high",
		}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fail(err.Error())
	}
	url := c.cfg.URL
	if url == "" {
		url = expoURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fail(err.Error())
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if c.cfg.AccessToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.AccessToken)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return fail(err.Error())
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != http.StatusOK {
		return fail(fmt.Sprintf("expo: status %d", res.StatusCode))
	}
	var reply struct {
		Data []expoTicket `json:"data"`
	}
	if err := json.Unmarshal(raw, &reply); err != nil || len(reply.Data) != len(msgs) {
		return fail("expo: unreadable reply")
	}
	out := make([]Result, len(msgs))
	for i, t := range reply.Data {
		if t.Status == "ok" {
			out[i] = Result{OK: true}
			continue
		}
		reason := t.Details.Error
		if reason == "" {
			reason = t.Message
		}
		out[i] = Result{Error: reason, Unregistered: t.Details.Error == "DeviceNotRegistered"}
	}
	return out
}
