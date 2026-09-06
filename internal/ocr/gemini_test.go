package ocr

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The real API is never called here. A stub stands in for it so the request we
// build and the reply we parse are both checked — the two halves that break
// when a provider changes shape.

// stubGemini stands a fake API in front of the provider and hands back the
// request it saw. The pointers are filled in during Extract, so they are only
// meaningful after the call.
func stubGemini(t *testing.T, reply string, status int) (g *gemini, seen **http.Request, body *[]byte) {
	t.Helper()
	var req *http.Request
	var sent []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent, _ = io.ReadAll(r.Body)
		req = r
		w.WriteHeader(status)
		io.WriteString(w, reply)
	}))
	t.Cleanup(srv.Close)
	g = NewGemini("test-key", "").(*gemini)
	g.base = srv.URL
	return g, &req, &sent
}

func geminiReply(inner string) string {
	out := map[string]any{"candidates": []any{map[string]any{
		"content": map[string]any{"parts": []any{map[string]any{"text": inner}}},
	}}}
	b, _ := json.Marshal(out)
	return string(b)
}

func TestGeminiSendsKeyInHeaderNotURL(t *testing.T) {
	g, seen, _ := stubGemini(t, geminiReply(`{"name":{"value":"A","confidence":1}}`), 200)
	if _, err := g.Extract(context.Background(), []byte{1, 2, 3}, "image/jpeg"); err != nil {
		t.Fatal(err)
	}
	if (*seen).Header.Get("x-goog-api-key") != "test-key" {
		t.Error("api key should travel in the x-goog-api-key header")
	}
	// A credential in a URL ends up in proxy logs and error strings.
	if strings.Contains((*seen).URL.String(), "test-key") {
		t.Errorf("api key must never appear in the URL: %s", (*seen).URL)
	}
}

func TestGeminiSendsTheImageAndAsksForJSON(t *testing.T) {
	g, _, body := stubGemini(t, geminiReply(`{"name":{"value":"A","confidence":1}}`), 200)
	image := []byte("pretend-jpeg-bytes")
	if _, err := g.Extract(context.Background(), image, "image/png"); err != nil {
		t.Fatal(err)
	}

	var sent map[string]any
	if err := json.Unmarshal(*body, &sent); err != nil {
		t.Fatalf("request body was not JSON: %v", err)
	}
	gen := sent["generationConfig"].(map[string]any)
	if gen["responseMimeType"] != "application/json" {
		t.Error("structured output should be requested, or the reply has to be scraped from prose")
	}
	if gen["temperature"].(float64) != 0 {
		t.Error("reading handwriting is transcription: temperature should be 0")
	}

	parts := sent["contents"].([]any)[0].(map[string]any)["parts"].([]any)
	inline := parts[1].(map[string]any)["inline_data"].(map[string]any)
	if inline["mime_type"] != "image/png" {
		t.Errorf("mime type not forwarded: %v", inline["mime_type"])
	}
	got, err := base64.StdEncoding.DecodeString(inline["data"].(string))
	if err != nil || string(got) != string(image) {
		t.Errorf("image not sent intact: %v %q", err, got)
	}
}

func TestGeminiParsesAndSanitisesTheReply(t *testing.T) {
	// Confidence over 1 and a stray course: the provider must not pass either
	// through, because the console trusts what this returns.
	g, _, _ := stubGemini(t, geminiReply(`{
		"name": {"value": "  Somchai  ", "confidence": 3},
		"dateOfBirth": {"value": "2016-06-02", "confidence": 0.9},
		"courses": ["chess", "MUSIC"]
	}`), 200)

	form, err := g.Extract(context.Background(), []byte{1}, "image/jpeg")
	if err != nil {
		t.Fatal(err)
	}
	if form.Name.Value != "Somchai" || form.Name.Confidence != 1 {
		t.Errorf("reply not sanitised: %+v", form.Name)
	}
	if len(form.Courses) != 1 || form.Courses[0] != "CHESS" {
		t.Errorf("courses not filtered to the printed three: %v", form.Courses)
	}
}

func TestGeminiErrorsDoNotLeakTheUpstreamBody(t *testing.T) {
	// The upstream error can quote the request, which is the child's data.
	g, _, _ := stubGemini(t, `{"error":{"message":"quota exceeded for project secret-123"}}`, 429)
	_, err := g.Extract(context.Background(), []byte{1}, "image/jpeg")
	if err == nil {
		t.Fatal("a non-200 should be an error")
	}
	if strings.Contains(err.Error(), "secret-123") {
		t.Errorf("upstream body must not be echoed: %v", err)
	}
}

func TestGeminiRejectsAReplyThatIsNotTheRequestedShape(t *testing.T) {
	g, _, _ := stubGemini(t, geminiReply(`not json at all`), 200)
	if _, err := g.Extract(context.Background(), []byte{1}, "image/jpeg"); err == nil {
		t.Error("a reply that is not the requested shape should fail, not half-parse")
	}
}

func TestGeminiHandlesNoCandidate(t *testing.T) {
	// A safety filter or an unreadable photo both arrive as an empty candidate
	// list rather than an HTTP error.
	g, _, _ := stubGemini(t, `{"candidates":[]}`, 200)
	if _, err := g.Extract(context.Background(), []byte{1}, "image/jpeg"); err == nil {
		t.Error("an empty candidate list should be an error")
	}
}
