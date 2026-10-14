package ocr

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func day(s string) time.Time {
	t, _ := time.Parse("2006-01-02", s)
	return t
}

// Google announced 3.8-flash doubles on 2027-01-01: before it, the current
// price and the coming one; from it, only the new one.
func TestAPriceMovesOnTheDayGoogleAnnounced(t *testing.T) {
	now, next, ok := PriceOn("gemini-3.8-flash", day("2026-10-14"))
	if !ok || now.InputPerMillion != 0.75 || now.OutputPerMillion != 3.75 {
		t.Fatalf("today = %+v ok=%v, want 0.75 / 3.75", now, ok)
	}
	if next == nil || next.From != "2027-01-01" || next.InputPerMillion != 1.50 {
		t.Fatalf("next = %+v, want 1.50 from 2027-01-01", next)
	}

	now, next, _ = PriceOn("gemini-3.8-flash", day("2027-01-01"))
	if now.InputPerMillion != 1.50 || now.OutputPerMillion != 7.50 || next != nil {
		t.Errorf("from 2027-01-01 = %+v next %+v, want 1.50 / 7.50 and nothing after", now, next)
	}
}

func TestAModelWithNoPriceOnFileHasNone(t *testing.T) {
	if _, _, ok := PriceOn("gemini-3.7-flash", day("2026-10-14")); ok {
		t.Error("a model Google lists no price for must show none, not a guess")
	}
}

// Only models that can read a photo are offered; aliases are left out because
// they move under you.
func TestOnlyModelsThatReadAPhotoAreOffered(t *testing.T) {
	for name, want := range map[string]bool{
		"gemini-3.8-flash":             true,
		"gemini-3.1-flash-lite":        true,
		"gemini-omni-1.1-flash":        true,
		"gemini-3.8-flash-tts":         false,
		"gemini-3.1-flash-image":       false,
		"gemini-embedding-2":           false,
		"gemini-3.8-live":              false,
		"gemini-flash-latest":          false,
		"gemini-robotics-er-2-preview": false,
		"gemma-4":                      false,
		"gemini-../../x":               false,
	} {
		if got := ForScanning(name); got != want {
			t.Errorf("ForScanning(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestTheModelListIsGooglesFilteredAndPaged(t *testing.T) {
	pages := map[string]string{
		"": `{"models":[
			{"name":"models/gemini-3.6-flash","displayName":"Gemini 3.6 Flash","supportedGenerationMethods":["generateContent"]},
			{"name":"models/gemini-3.8-flash-tts","supportedGenerationMethods":["generateContent"]},
			{"name":"models/gemini-embedding-2","supportedGenerationMethods":["embedContent"]}
		],"nextPageToken":"p2"}`,
		"p2": `{"models":[
			{"name":"models/gemini-3.8-flash","displayName":"Gemini 3.8 Flash","supportedGenerationMethods":["generateContent","countTokens"]},
			{"name":"models/gemini-3.9-flash","displayName":"No generate","supportedGenerationMethods":["countTokens"]}
		]}`,
	}
	var keys []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		keys = append(keys, r.Header.Get("x-goog-api-key"))
		if r.URL.Query().Get("key") != "" {
			t.Error("the key must not travel in the URL")
		}
		io.WriteString(w, pages[r.URL.Query().Get("pageToken")])
	}))
	t.Cleanup(srv.Close)
	g := NewGemini("test-key", "").(*gemini)
	g.modelsBase = srv.URL

	got, err := g.ListModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Name != "gemini-3.8-flash" || got[1].Name != "gemini-3.6-flash" {
		t.Fatalf("models = %+v, want 3.8-flash then 3.6-flash", got)
	}
	if got[0].DisplayName != "Gemini 3.8 Flash" {
		t.Errorf("displayName = %q", got[0].DisplayName)
	}
	if len(keys) != 2 || keys[0] != "test-key" || keys[1] != "test-key" {
		t.Errorf("want both pages fetched with the key in the header, got %q", keys)
	}
}

func TestAFailedModelListGivesTheStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		io.WriteString(w, `{"error":{"message":"quotes the request"}}`)
	}))
	t.Cleanup(srv.Close)
	g := NewGemini("test-key", "").(*gemini)
	g.modelsBase = srv.URL

	_, err := g.ListModels(context.Background())
	var status *StatusError
	if !errors.As(err, &status) || status.Status != 403 {
		t.Fatalf("err = %v, want status 403", err)
	}
	if fmt.Sprint(err) != "gemini: status 403" {
		t.Errorf("the upstream body must not be in the error: %v", err)
	}
}
