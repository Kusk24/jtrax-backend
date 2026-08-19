package lichess_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Kusk24/jtrax-backend/internal/lichess"
)

// PKCE is worth nothing if the challenge is not really derived from the
// verifier, and that is exactly the kind of thing that keeps working while
// being wrong. This recomputes it independently rather than trusting the
// function to agree with itself.
func TestCodeChallengeIsS256OfVerifier(t *testing.T) {
	const verifier = "MEwzMzk4MDktYzFhNi00YzE1LWE4ZDktOWM5ZjMwZGY5ZjZl"
	sum := sha256.Sum256([]byte(verifier))
	want := base64.RawURLEncoding.EncodeToString(sum[:])

	if got := lichess.CodeChallenge(verifier); got != want {
		t.Fatalf("CodeChallenge = %q, want %q", got, want)
	}
	// Base64url, not standard base64: a "+" or "/" here would be mangled in a
	// query string and the exchange would fail in a way that looks like a
	// Lichess outage.
	if strings.ContainsAny(lichess.CodeChallenge(verifier), "+/=") {
		t.Error("challenge is not base64url without padding")
	}
}

func TestNewVerifierIsLongAndUnique(t *testing.T) {
	seen := map[string]bool{}
	for range 50 {
		v, err := lichess.NewVerifier()
		if err != nil {
			t.Fatal(err)
		}
		// RFC 7636 requires 43-128 characters.
		if len(v) < 43 || len(v) > 128 {
			t.Fatalf("verifier length %d out of the RFC 7636 range", len(v))
		}
		if seen[v] {
			t.Fatal("NewVerifier repeated itself")
		}
		seen[v] = true
	}
}

func TestAuthorizeURLCarriesPKCEAndNotTheVerifier(t *testing.T) {
	c := &lichess.Client{BaseURL: "https://lichess.org"}
	const verifier = "the-secret-verifier-value-that-must-not-travel"
	challenge := lichess.CodeChallenge(verifier)

	raw := c.AuthorizeURL("jtrax.app", "https://api.example/cb", "st4te", challenge, lichess.PlayScopes)
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()

	for k, want := range map[string]string{
		"response_type":         "code",
		"client_id":             "jtrax.app",
		"redirect_uri":          "https://api.example/cb",
		"code_challenge_method": "S256",
		"code_challenge":        challenge,
		"state":                 "st4te",
		"scope":                 "board:play challenge:write",
	} {
		if got := q.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	// The verifier travelling here would defeat the entire mechanism.
	if strings.Contains(raw, verifier) {
		t.Fatal("the code verifier leaked into the authorize URL")
	}
}

func TestExchangeSendsVerifierAndReturnsToken(t *testing.T) {
	var gotForm url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/token" {
			t.Errorf("exchange hit %s", r.URL.Path)
		}
		_ = r.ParseForm()
		gotForm = r.PostForm
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"token_type":"Bearer","access_token":"lio_abc","expires_in":31536000}`))
	}))
	defer srv.Close()

	c := &lichess.Client{BaseURL: srv.URL}
	tok, err := c.Exchange(context.Background(), "jtrax.app", "https://api.example/cb", "code-123", "verifier-456")
	if err != nil {
		t.Fatal(err)
	}
	if tok.AccessToken != "lio_abc" {
		t.Errorf("access token = %q", tok.AccessToken)
	}
	// A year out, give or take. The point is that an expiry was computed at
	// all: without it nothing can warn a student before the token dies.
	if tok.ExpiresAt.IsZero() {
		t.Error("ExpiresAt was not derived from expires_in")
	}
	if got := gotForm.Get("code_verifier"); got != "verifier-456" {
		t.Errorf("code_verifier = %q", got)
	}
	if got := gotForm.Get("grant_type"); got != "authorization_code" {
		t.Errorf("grant_type = %q", got)
	}
}

// A refused exchange must not hand the upstream body to the caller: it can echo
// the authorization code and the redirect back out again.
func TestExchangeFailureDoesNotLeakTheCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
	}))
	defer srv.Close()

	c := &lichess.Client{BaseURL: srv.URL}
	_, err := c.Exchange(context.Background(), "jtrax.app", "https://api.example/cb", "SECRET-CODE", "v")
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "SECRET-CODE") {
		t.Errorf("the authorization code leaked into the error: %v", err)
	}
}

func TestExchangeRejectsEmptyToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"token_type":"Bearer","expires_in":10}`))
	}))
	defer srv.Close()

	c := &lichess.Client{BaseURL: srv.URL}
	if _, err := c.Exchange(context.Background(), "id", "uri", "code", "v"); err == nil {
		t.Fatal("a response with no access token was accepted")
	}
}
