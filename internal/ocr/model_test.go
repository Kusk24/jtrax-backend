package ocr

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// Switching the model at run time, and the connection test behind the console's
// Test button. Uses the stub from gemini_test.go: the real API is never called.

func TestGeminiTestIsATextCallThatReportsTheStatus(t *testing.T) {
	g, seen, body := stubGemini(t, `{"error":{"message":"models/x is not found"}}`, 404)
	err := g.Test(context.Background())
	var status *StatusError
	if !errors.As(err, &status) || status.Status != 404 {
		t.Fatalf("want a 404 StatusError, got %v", err)
	}
	if strings.Contains(err.Error(), "not found") {
		t.Errorf("the upstream body leaked into the error: %v", err)
	}
	if (*seen).Header.Get("x-goog-api-key") != "test-key" {
		t.Error("the test must go through the same key a scan uses")
	}
	// No image: a test must not cost what a scan costs.
	if strings.Contains(string(*body), "inline_data") {
		t.Errorf("the test sent an image: %s", *body)
	}
}

func TestGeminiTestPassesWhenTheModelAnswers(t *testing.T) {
	g, _, _ := stubGemini(t, geminiReply("OK"), 200)
	if err := g.Test(context.Background()); err != nil {
		t.Fatal(err)
	}
	// An empty answer still means the model was reached.
	g, _, _ = stubGemini(t, `{"candidates":[]}`, 200)
	if err := g.Test(context.Background()); err != nil {
		t.Fatalf("an empty reply is still a working model: %v", err)
	}
}

func TestGeminiWithModelSwitchesOnlyToASafeName(t *testing.T) {
	g := NewGemini("test-key", "").(*gemini)
	if got := g.WithModel("gemini-3.5-flash-lite").Name(); got != "gemini/gemini-3.5-flash-lite" {
		t.Errorf("switch: got %s", got)
	}
	if g.Name() == "gemini/gemini-3.5-flash-lite" {
		t.Error("WithModel changed the original instead of a copy")
	}
	// Into the URL path it goes, so a slash or query must not get through.
	for _, bad := range []string{"../files", "x?alt=1", "", "UPPER"} {
		if got := g.WithModel(bad).Name(); got != g.Name() {
			t.Errorf("%q was accepted: %s", bad, got)
		}
	}
}
