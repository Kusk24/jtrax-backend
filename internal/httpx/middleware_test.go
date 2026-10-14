package httpx

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A browser asks before a cross-origin call with any method but GET or POST.
// Every method a route is mounted with must be in the answer, or the browser
// refuses the call itself and the server never sees it.
func TestCORSAllowsEveryMethodTheAPIUses(t *testing.T) {
	h := CORS([]string{"http://localhost:8081"}, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	req := httptest.NewRequest(http.MethodOptions, "/api/v1/notification-settings", nil)
	req.Header.Set("Origin", "http://localhost:8081")
	req.Header.Set("Access-Control-Request-Method", "PUT")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	allowed := rec.Header().Get("Access-Control-Allow-Methods")
	for _, m := range []string{"GET", "POST", "PUT", "PATCH", "DELETE"} {
		if !strings.Contains(allowed, m) {
			t.Errorf("%s is not allowed cross-origin: %q", m, allowed)
		}
	}
	if rec.Code != http.StatusNoContent {
		t.Errorf("preflight: want 204, got %d", rec.Code)
	}
}

// An origin not on the list gets no CORS headers at all.
func TestCORSIgnoresAnUnknownOrigin(t *testing.T) {
	h := CORS([]string{"http://localhost:8081"}, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	req := httptest.NewRequest(http.MethodOptions, "/api/v1/notification-settings", nil)
	req.Header.Set("Origin", "https://evil.example")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("an unknown origin must not be allowed")
	}
}
