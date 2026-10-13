package api_test

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/Kusk24/jtrax-backend/internal/ocr"
)

/* A failed scan says why. Every provider failure used to answer the same
 * "could not read the card", so a tester could not tell a refused key from a
 * blurry photo without the server log.
 */

func TestAFailedCardScanSaysWhy(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantReason string
		wantStatus float64
		wantWords  string
	}{
		{"model withdrawn", fmt.Errorf("gemini: %w", &ocr.StatusError{Status: 404}),
			"model_unavailable", 404, "scan model is not available (status 404)"},
		{"key refused", fmt.Errorf("gemini: %w", &ocr.StatusError{Status: 403}),
			"key_rejected", 403, "refused the server's key (status 403)"},
		{"quota", fmt.Errorf("gemini: %w", &ocr.StatusError{Status: 429}),
			"quota", 429, "limit is used up (status 429)"},
		{"provider down", fmt.Errorf("gemini: %w", &ocr.StatusError{Status: 503}),
			"provider_error", 503, "answered with an error (status 503)"},
		{"nothing read", ocr.ErrNoCandidate,
			"no_answer", 0, "gave no answer for this photo"},
		{"timed out", context.DeadlineExceeded,
			"unreachable", 0, "could not be reached"},
		{"no route", &url.Error{Op: "Post", URL: "https://example.invalid", Err: errors.New("dial tcp: no such host")},
			"unreachable", 0, "could not be reached"},
		{"bad reply", errors.New("gemini: reply was not the requested shape"),
			"provider_error", 0, "answer could not be used"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv, id := cardServer(t, &fakeCardReader{err: c.err})
			status, out := postScan(t, srv, "", "image", onePixelPNG, "/api/v1/public/tournaments/"+id+"/scan-id")
			if status != 502 {
				t.Fatalf("want 502, got %d (%v)", status, out)
			}
			if out["reason"] != c.wantReason {
				t.Errorf("reason = %v, want %s", out["reason"], c.wantReason)
			}
			got, _ := out["status"].(float64)
			if got != c.wantStatus {
				t.Errorf("status = %v, want %v", out["status"], c.wantStatus)
			}
			msg, _ := out["error"].(string)
			if !strings.HasPrefix(msg, "could not read the card: ") || !strings.Contains(msg, c.wantWords) {
				t.Errorf("error = %q, want it to say %q", msg, c.wantWords)
			}
		})
	}
}

// The provider's own reply can quote the document. Only the code and the
// status leave the server, whatever the error carried.
func TestAFailedScanDoesNotEchoTheProvidersError(t *testing.T) {
	secret := "Somchai Jaidee 1-2345-67890-12-3"
	srv, id := cardServer(t, &fakeCardReader{err: errors.New("gemini: " + secret)})
	_, out := postScan(t, srv, "", "image", onePixelPNG, "/api/v1/public/tournaments/"+id+"/scan-id")
	for k, v := range out {
		if s, ok := v.(string); ok && strings.Contains(s, "Somchai") {
			t.Errorf("%s = %q echoes the provider's error", k, s)
		}
	}
}

// Failures before the provider is called carry a code too, so a screen can say
// them in its own language.
func TestTheCardScansOtherFailuresHaveCodes(t *testing.T) {
	t.Run("not an image", func(t *testing.T) {
		srv, id := cardServer(t, &fakeCardReader{card: &ocr.IDCard{}})
		status, out := postScan(t, srv, "", "image", []byte("%PDF-1.4 not an image"),
			"/api/v1/public/tournaments/"+id+"/scan-id")
		if status != 415 || out["reason"] != "not_an_image" {
			t.Errorf("got %d %v, want 415 not_an_image", status, out["reason"])
		}
	})
	t.Run("no date of birth", func(t *testing.T) {
		srv, id := cardServer(t, &fakeCardReader{card: &ocr.IDCard{}})
		status, out := postScan(t, srv, "", "image", onePixelPNG, "/api/v1/public/tournaments/"+id+"/scan-id")
		if status != 422 || out["reason"] != "no_date_of_birth" {
			t.Errorf("got %d %v, want 422 no_date_of_birth", status, out["reason"])
		}
	})
	t.Run("cannot read cards", func(t *testing.T) {
		srv, id := cardServer(t, &formOnlyScanner{})
		status, out := postScan(t, srv, "", "image", onePixelPNG, "/api/v1/public/tournaments/"+id+"/scan-id")
		if status != 503 || out["reason"] != "not_configured" {
			t.Errorf("got %d %v, want 503 not_configured", status, out["reason"])
		}
	})
}

// The staff form scan says why too, so the admin console can.
func TestAFailedFormScanSaysWhy(t *testing.T) {
	t.Run("provider", func(t *testing.T) {
		srv := scanServer(t, &fakeScanner{err: fmt.Errorf("gemini: %w", &ocr.StatusError{Status: 404})})
		status, out := postScan(t, srv, tokenFor(t, srv, "admin@jca.ac.th"), "image", onePixelPNG)
		msg, _ := out["error"].(string)
		if status != 502 || out["reason"] != "model_unavailable" || out["status"] != float64(404) ||
			!strings.HasPrefix(msg, "could not read the form: ") {
			t.Errorf("got %d %v", status, out)
		}
	})
	t.Run("before the provider", func(t *testing.T) {
		srv := scanServer(t, &fakeScanner{form: &ocr.Form{}})
		token := tokenFor(t, srv, "admin@jca.ac.th")
		for _, c := range []struct {
			field  string
			data   []byte
			status int
			reason string
		}{
			{"image", []byte("%PDF-1.7\n"), 415, "not_an_image"},
			{"image", []byte{}, 400, "no_image"},
			{"notimage", onePixelPNG, 400, "no_image"},
		} {
			status, out := postScan(t, srv, token, c.field, c.data)
			if status != c.status || out["reason"] != c.reason {
				t.Errorf("%s %q: got %d %v, want %d %s", c.field, c.data, status, out["reason"], c.status, c.reason)
			}
		}
	})
	t.Run("not configured", func(t *testing.T) {
		srv := scanServer(t, nil)
		status, out := postScan(t, srv, tokenFor(t, srv, "admin@jca.ac.th"), "image", onePixelPNG)
		if status != 503 || out["reason"] != "not_configured" {
			t.Errorf("got %d %v, want 503 not_configured", status, out["reason"])
		}
	})
}
