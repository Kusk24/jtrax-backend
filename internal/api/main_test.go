package api_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

// TestMain keeps the suite off the real Lichess. The puzzle endpoints top the
// bank up from lichess.org when it runs low, and a test that empties the bank
// would otherwise call someone else's rate-limited API — passing or failing on
// their availability, not on this code. Unless a test points LICHESS_API_BASE
// at its own stub, Lichess answers 503 here, which is the outage case.
func TestMain(m *testing.M) {
	if os.Getenv("LICHESS_API_BASE") != "" {
		os.Exit(m.Run())
	}
	offline := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no network in tests", http.StatusServiceUnavailable)
	}))
	os.Setenv("LICHESS_API_BASE", offline.URL)
	code := m.Run()
	offline.Close()
	os.Exit(code)
}
