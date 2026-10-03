package push

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// expoStub plays Expo: it answers each message with the ticket `ticket` picks,
// and remembers every batch and header it was sent.
func expoStub(t *testing.T, ticket func(to string) string) (*httptest.Server, *[][]map[string]any, *[]string) {
	t.Helper()
	var batches [][]map[string]any
	var auth []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var msgs []map[string]any
		json.Unmarshal(raw, &msgs)
		batches = append(batches, msgs)
		auth = append(auth, r.Header.Get("Authorization"))
		tickets := make([]string, len(msgs))
		for i, m := range msgs {
			tickets[i] = ticket(m["to"].(string))
		}
		io.WriteString(w, `{"data":[`+strings.Join(tickets, ",")+`]}`)
	}))
	t.Cleanup(srv.Close)
	return srv, &batches, &auth
}

const ok = `{"status":"ok","id":"t1"}`
const gone = `{"status":"error","message":"not registered","details":{"error":"DeviceNotRegistered"}}`

func TestSendReadsEachTicket(t *testing.T) {
	srv, batches, auth := expoStub(t, func(to string) string {
		if to == "ExponentPushToken[old]" {
			return gone
		}
		return ok
	})
	c := New(Config{URL: srv.URL})
	res := c.Send(context.Background(), []Message{
		{To: "ExponentPushToken[new]", Title: "Checked in", Body: "Penny has arrived.", Data: map[string]any{"studentId": "stu_penny"}},
		{To: "ExponentPushToken[old]", Title: "Checked in", Body: "Penny has arrived."},
	})
	if len(res) != 2 || !res[0].OK || res[1].OK || !res[1].Unregistered {
		t.Fatalf("results: %+v", res)
	}
	sent := (*batches)[0][0]
	if sent["title"] != "Checked in" || sent["channelId"] != "default" || sent["sound"] != "default" {
		t.Fatalf("message as sent: %v", sent)
	}
	if data, _ := sent["data"].(map[string]any); data["studentId"] != "stu_penny" {
		t.Fatalf("data should travel with the message: %v", sent["data"])
	}
	// No access token configured: none sent.
	if (*auth)[0] != "" {
		t.Fatalf("unexpected Authorization header %q", (*auth)[0])
	}
}

func TestSendUsesTheAccessTokenWhenThereIsOne(t *testing.T) {
	srv, _, auth := expoStub(t, func(string) string { return ok })
	New(Config{URL: srv.URL, AccessToken: "expo-secret"}).Send(context.Background(), []Message{{To: "ExponentPushToken[a]"}})
	if (*auth)[0] != "Bearer expo-secret" {
		t.Fatalf("Authorization: %q", (*auth)[0])
	}
}

// Expo takes 100 at a time, so a school-wide announcement is split.
func TestSendSplitsIntoBatchesOfAHundred(t *testing.T) {
	srv, batches, _ := expoStub(t, func(string) string { return ok })
	msgs := make([]Message, 230)
	for i := range msgs {
		msgs[i] = Message{To: "ExponentPushToken[x]"}
	}
	res := New(Config{URL: srv.URL}).Send(context.Background(), msgs)
	if len(*batches) != 3 || len((*batches)[0]) != 100 || len((*batches)[2]) != 30 || len(res) != 230 {
		t.Fatalf("batches %d, results %d", len(*batches), len(res))
	}
}

func TestAnUnreachableServiceFailsTheBatchWithoutPanicking(t *testing.T) {
	res := New(Config{URL: "http://127.0.0.1:1"}).Send(context.Background(), []Message{{To: "ExponentPushToken[a]"}})
	if len(res) != 1 || res[0].OK || res[0].Error == "" {
		t.Fatalf("results: %+v", res)
	}
}

func TestIsExpoToken(t *testing.T) {
	for token, want := range map[string]bool{
		"ExponentPushToken[abc]": true, "ExpoPushToken[abc]": true,
		"https://fcm.googleapis.com/fcm/send/x": false, "": false,
	} {
		if IsExpoToken(token) != want {
			t.Errorf("%q: want %v", token, want)
		}
	}
}
