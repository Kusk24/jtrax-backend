// Exercise the school's route table against the net/http behavior existing clients use.
package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/Kusk24/jtrax-backend/internal/mail"
)

type patterns []string

func (p *patterns) HandleFunc(pattern string, _ func(http.ResponseWriter, *http.Request)) {
	*p = append(*p, pattern)
}

func TestGinRouteContracts(t *testing.T) {
	var routes patterns
	registerRoutes(&routes, nil, mail.Config{}, nil, nil)
	old := http.NewServeMux()
	current := newRouter()
	for _, pattern := range routes {
		// Include path values in the result so two matching handler names alone
		// cannot hide a missing or incorrectly decoded student or resource ID.
		handler := func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			fmt.Fprint(w, r.Pattern)
			for _, part := range strings.Split(pattern, "/") {
				if name, ok := strings.CutPrefix(part, "{"); ok {
					fmt.Fprint(w, " ", r.PathValue(strings.TrimSuffix(name, "}")))
				}
			}
		}
		old.HandleFunc(pattern, handler)
		current.HandleFunc(pattern, handler)
	}
	check := func(method, path string) {
		t.Helper()
		request := func(h http.Handler) *httptest.ResponseRecorder {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest(method, path, nil))
			return w
		}
		want, got := request(old), request(current)
		if got.Code != want.Code || got.Body.String() != want.Body.String() ||
			!reflect.DeepEqual(got.Result().Header, want.Result().Header) {
			t.Fatalf("%s %s: want %d %v %q, got %d %v %q", method, path,
				want.Code, want.Result().Header, want.Body, got.Code, got.Result().Header, got.Body)
		}
	}
	for _, pattern := range routes {
		method, path, _ := strings.Cut(pattern, " ")
		parts := strings.Split(path, "/")
		for i, part := range parts {
			if strings.HasPrefix(part, "{") {
				parts[i] = fmt.Sprintf("sample-%d", i)
			}
		}
		path = strings.Join(parts, "/")
		check(method, path)
		check("OPTIONS", path)
		check("HEAD", path)
	}
	for _, path := range []string{"/unknown", "/health/", "//health", "/api/../health",
		"/api/v1/students/a%2Fb/history", "/api/v1/students/a+b/history"} {
		check("GET", path)
	}
	t.Logf("compared %d registered routes", len(routes))
}

// The route comparison above exercises each registered handler. This independent
// inventory also detects a whole feature being omitted from registerRoutes.
func TestPublishedRoutesRemainAvailable(t *testing.T) {
	raw, err := os.ReadFile("testdata/published_routes.txt")
	if err != nil {
		t.Fatal(err)
	}
	for _, stripeEnabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("stripe=%t", stripeEnabled), func(t *testing.T) {
			t.Setenv("STRIPE_SECRET_KEY", "")
			t.Setenv("STRIPE_WEBHOOK_SECRET", "")
			if stripeEnabled {
				t.Setenv("STRIPE_SECRET_KEY", "test-key")
				t.Setenv("STRIPE_WEBHOOK_SECRET", "test-secret")
			}
			var routes patterns
			registerRoutes(&routes, nil, mail.Config{}, nil, nil)
			available := make(map[string]bool, len(routes))
			for _, route := range routes {
				available[route] = true
			}
			for _, route := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
				want := stripeEnabled || route != "POST /api/v1/stripe/webhook"
				if available[route] != want {
					t.Errorf("%s: availability = %t, want %t", route, available[route], want)
				}
			}
		})
	}
}
