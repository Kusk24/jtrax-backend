package api_test

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

/* A stand-in for Lichess's OAuth and account endpoints.

   The token it issues is a recognisable string so a test can assert the thing
   that matters most about it: that it never comes back out of the API. */

const stubAccessToken = "lio_stub_secret_token_value"

type oauthStub struct {
	mu       sync.Mutex
	srv      *httptest.Server
	username string
	// lastForm is the exchange body, so a test can check the verifier really
	// travelled and matched.
	lastForm  url.Values
	exchanges int
	revoked   int
}

func newOAuthStub(t *testing.T) *oauthStub {
	s := &oauthStub{username: "PennyPlays"}
	mux := http.NewServeMux()

	mux.HandleFunc("POST /api/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		s.mu.Lock()
		s.lastForm = r.PostForm
		s.exchanges++
		s.mu.Unlock()
		if r.PostForm.Get("code") == "bad-code" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token_type": "Bearer", "access_token": stubAccessToken, "expires_in": 31536000,
		})
	})
	mux.HandleFunc("GET /api/account", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+stubAccessToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		s.mu.Lock()
		name := s.username
		s.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"id": strings.ToLower(name), "username": name})
	})
	mux.HandleFunc("DELETE /api/token", func(w http.ResponseWriter, _ *http.Request) {
		s.mu.Lock()
		s.revoked++
		s.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	// Ratings are read after a grant, so the profile endpoint has to exist too.
	mux.HandleFunc("GET /api/user/{name}", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": strings.ToLower(name), "username": name,
			"profile": map[string]any{"bio": ""},
			"perfs":   map[string]any{"blitz": map[string]any{"rating": 1300, "games": 40}},
		})
	})
	mux.HandleFunc("POST /api/users", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[]`))
	})

	s.srv = httptest.NewServer(mux)
	t.Cleanup(s.srv.Close)
	return s
}

func (s *oauthStub) form() url.Values {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastForm
}

func (s *oauthStub) revokeCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.revoked
}

// newOAuthServer wires a server with play access fully configured.
//
// PUBLIC_API_URL is a fixed string rather than the test server's own address
// because the callback is driven directly here; what matters is that the same
// value reaches both the authorize URL and the exchange.
func newOAuthServer(t *testing.T) (*client, *oauthStub) {
	t.Helper()
	stub := newOAuthStub(t)
	t.Setenv("LICHESS_API_BASE", stub.srv.URL)
	t.Setenv("PUBLIC_API_URL", "https://api.test")
	t.Setenv("APP_URL", "https://portal.test")
	t.Setenv("LICHESS_TOKEN_KEY", base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef")))
	return &client{t: t, srv: newServer(t)}, stub
}

// startFlow runs the authenticated half and returns the state parameter.
func startFlow(t *testing.T, c *client, body any) (state, challenge string) {
	t.Helper()
	status, obj, _ := c.do("POST", "/api/v1/lichess/oauth/start", body)
	if status != 200 {
		t.Fatalf("oauth start: status %d (%v)", status, obj)
	}
	raw, _ := obj["authorizeUrl"].(string)
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("authorizeUrl %q: %v", raw, err)
	}
	return u.Query().Get("state"), u.Query().Get("code_challenge")
}

/* ---- starting the flow ---- */

func TestOAuthStartBuildsAPKCEAuthorizeURL(t *testing.T) {
	base, _ := newOAuthServer(t)
	penny := asStudent(t, base, "penny@jca.ac.th")

	status, obj, _ := penny.do("POST", "/api/v1/lichess/oauth/start",
		map[string]string{"returnTo": "https://portal.test/student"})
	if status != 200 {
		t.Fatalf("status %d (%v)", status, obj)
	}
	u, err := url.Parse(obj["authorizeUrl"].(string))
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if q.Get("response_type") != "code" || q.Get("code_challenge_method") != "S256" {
		t.Errorf("not a PKCE authorization request: %v", q)
	}
	if q.Get("redirect_uri") != "https://api.test/api/v1/lichess/oauth/callback" {
		t.Errorf("redirect_uri = %q", q.Get("redirect_uri"))
	}
	// The smallest scopes that can play a game. A widening here is a privacy
	// regression on a child's account and should fail the build.
	if q.Get("scope") != "board:play challenge:write" {
		t.Errorf("scope = %q", q.Get("scope"))
	}
	if q.Get("state") == "" || q.Get("code_challenge") == "" {
		t.Error("state and code_challenge are both required")
	}
}

func TestOAuthStartRequiresSignIn(t *testing.T) {
	base, _ := newOAuthServer(t)
	anon := &client{t: t, srv: base.srv}
	if status, _, _ := anon.do("POST", "/api/v1/lichess/oauth/start", nil); status != 401 {
		t.Fatalf("status %d, want 401", status)
	}
}

// A parent runs the flow for a young child — that is the under-13 case, since
// Lichess requires an account holder to be 13. They must not be able to run it
// for somebody else's child.
func TestOAuthStartParentScopedToOwnChildren(t *testing.T) {
	base, _ := newOAuthServer(t)

	// Both seeded pupils belong to Sandy, so the negative case needs a pupil
	// who belongs to nobody.
	admin := asStudent(t, base, "admin@jca.ac.th")
	status, created, _ := admin.do("POST", "/api/v1/students", map[string]any{
		"name": "Unrelated Child", "current_level": "Beginner",
	})
	if status != 201 {
		t.Fatalf("creating an unrelated student: %d (%v)", status, created)
	}
	stranger := created["student_id"].(string)

	sandy := asStudent(t, base, "sandy01234@gmail.com")
	if status, _, _ := sandy.do("POST", "/api/v1/lichess/oauth/start",
		map[string]string{"studentId": "stu_penny"}); status != 200 {
		t.Fatalf("a parent should be able to link their own child: status %d", status)
	}
	if status, _, _ := sandy.do("POST", "/api/v1/lichess/oauth/start",
		map[string]string{"studentId": stranger}); status != 403 {
		t.Fatalf("status %d for another family's child, want 403", status)
	}
}

/* ---- the callback ---- */

func TestOAuthCallbackStoresTheGrantAndVerifies(t *testing.T) {
	base, stub := newOAuthServer(t)
	penny := asStudent(t, base, "penny@jca.ac.th")
	state, challenge := startFlow(t, penny, map[string]string{"returnTo": "https://portal.test/student"})

	// Follow nothing: the callback answers with a redirect and the assertion is
	// about where it points.
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	res, err := noRedirect.Get(base.srv.URL + "/api/v1/lichess/oauth/callback?code=good&state=" + url.QueryEscape(state))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("callback status %d, want 303", res.StatusCode)
	}
	if loc := res.Header.Get("Location"); !strings.HasPrefix(loc, "https://portal.test/student") ||
		!strings.Contains(loc, "lichess=connected") {
		t.Errorf("Location = %q", loc)
	}

	// The verifier really travelled, and really produced the challenge that was
	// advertised. Without this the flow would work while providing no proof.
	verifier := stub.form().Get("code_verifier")
	if verifier == "" {
		t.Fatal("no code_verifier reached the exchange")
	}
	sum := sha256.Sum256([]byte(verifier))
	if want := base64.RawURLEncoding.EncodeToString(sum[:]); want != challenge {
		t.Errorf("challenge %q is not S256 of the verifier sent", challenge)
	}

	// The grant is proof of ownership, so the link is verified with no bio code.
	status, obj, _ := penny.do("GET", "/api/v1/lichess/me", nil)
	if status != 200 || obj["linked"] != true {
		t.Fatalf("me: status %d (%v)", status, obj)
	}
	link := obj["link"].(map[string]any)
	if link["verified"] != true {
		t.Errorf("an OAuth grant must verify the link: %v", link)
	}
	if link["verifyCode"] != nil {
		t.Errorf("a granted link must not carry a bio code: %v", link["verifyCode"])
	}

	status, obj, _ = penny.do("GET", "/api/v1/lichess/play-status", nil)
	if status != 200 || obj["canPlay"] != true {
		t.Fatalf("play-status: %d (%v)", status, obj)
	}
	if obj["expiresAt"] == nil || obj["expiresAt"] == "" {
		t.Error("expiry must be reported: the token cannot be refreshed")
	}
}

// "Managed" means an adult holds the Lichess password, which is how an under-13
// pupil plays at all. It is a safeguarding fact about a child, so claiming it
// for a student who linked their own account is not a cosmetic error.
func TestSelfLinkedStudentIsNotMarkedManaged(t *testing.T) {
	base, _ := newOAuthServer(t)
	penny := asStudent(t, base, "penny@jca.ac.th")
	state, _ := startFlow(t, penny, nil)
	completeCallback(t, base, state, "good")

	_, obj, _ := penny.do("GET", "/api/v1/lichess/play-status", nil)
	if obj["managed"] != false {
		t.Errorf("a pupil who linked their own account was marked managed: %v", obj)
	}
}

// A parent running the flow for a young child *is* managed, and that has to be
// visible rather than inferred.
func TestParentLinkedChildIsMarkedManaged(t *testing.T) {
	base, _ := newOAuthServer(t)
	sandy := asStudent(t, base, "sandy01234@gmail.com")
	state, _ := startFlow(t, sandy, map[string]string{"studentId": "stu_penny"})
	completeCallback(t, base, state, "good")

	penny := asStudent(t, base, "penny@jca.ac.th")
	_, obj, _ := penny.do("GET", "/api/v1/lichess/play-status", nil)
	if obj["managed"] != true {
		t.Errorf("a parent-run grant was not marked managed: %v", obj)
	}
}

// The token is the most dangerous value in the database — it can resign a
// child's games. It must never appear in a response.
func TestOAuthTokenNeverLeavesTheServer(t *testing.T) {
	base, _ := newOAuthServer(t)
	penny := asStudent(t, base, "penny@jca.ac.th")
	state, _ := startFlow(t, penny, nil)
	completeCallback(t, base, state, "good")

	for _, path := range []string{
		"/api/v1/lichess/me",
		"/api/v1/lichess/links",
		"/api/v1/lichess/play-status",
	} {
		req, _ := http.NewRequest("GET", base.srv.URL+path, nil)
		req.Header.Set("Authorization", "Bearer "+penny.token)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body := make([]byte, 8192)
		n, _ := res.Body.Read(body)
		res.Body.Close()
		if strings.Contains(string(body[:n]), stubAccessToken) {
			t.Fatalf("%s leaked the access token", path)
		}
	}
}

// Single use. Two browsers replaying one callback must not both succeed.
func TestOAuthCallbackStateIsSingleUse(t *testing.T) {
	base, _ := newOAuthServer(t)
	penny := asStudent(t, base, "penny@jca.ac.th")
	state, _ := startFlow(t, penny, nil)

	if code := completeCallback(t, base, state, "good"); code != http.StatusSeeOther && code != http.StatusOK {
		t.Fatalf("first callback: %d", code)
	}
	if code := completeCallback(t, base, state, "good"); code != http.StatusBadRequest {
		t.Fatalf("replayed callback: %d, want 400", code)
	}
}

func TestOAuthCallbackRejectsUnknownState(t *testing.T) {
	base, _ := newOAuthServer(t)
	if code := completeCallback(t, base, "never-issued", "good"); code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", code)
	}
}

// The student pressing "cancel" on Lichess is an outcome, not a failure.
func TestOAuthCallbackHandlesDecline(t *testing.T) {
	base, _ := newOAuthServer(t)
	penny := asStudent(t, base, "penny@jca.ac.th")
	state, _ := startFlow(t, penny, map[string]string{"returnTo": "https://portal.test/student"})

	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	res, err := noRedirect.Get(base.srv.URL + "/api/v1/lichess/oauth/callback?error=access_denied&state=" + url.QueryEscape(state))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if !strings.Contains(res.Header.Get("Location"), "lichess=declined") {
		t.Errorf("Location = %q, want a declined outcome", res.Header.Get("Location"))
	}
	// Nothing was granted, so nothing may claim play access.
	if status, obj, _ := penny.do("GET", "/api/v1/lichess/play-status", nil); status != 200 || obj["canPlay"] == true {
		t.Errorf("a declined grant must not enable play: %v", obj)
	}
}

// An open redirect on the academy's own domain would be a usable phishing hop.
func TestOAuthReturnToMustBeAKnownPortal(t *testing.T) {
	base, _ := newOAuthServer(t)
	penny := asStudent(t, base, "penny@jca.ac.th")
	state, _ := startFlow(t, penny, map[string]string{"returnTo": "https://evil.example/steal"})

	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	res, err := noRedirect.Get(base.srv.URL + "/api/v1/lichess/oauth/callback?code=good&state=" + url.QueryEscape(state))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if loc := res.Header.Get("Location"); strings.Contains(loc, "evil.example") {
		t.Fatalf("callback redirected to an unlisted origin: %q", loc)
	}
}

// Disconnecting must revoke on Lichess, not just forget locally: otherwise the
// grant lives on the student's account forever.
func TestUnlinkRevokesTheGrant(t *testing.T) {
	base, stub := newOAuthServer(t)
	penny := asStudent(t, base, "penny@jca.ac.th")
	state, _ := startFlow(t, penny, nil)
	completeCallback(t, base, state, "good")

	if status, _, _ := penny.do("DELETE", "/api/v1/lichess/link", nil); status != 200 {
		t.Fatalf("unlink status %d", status)
	}
	if got := stub.revokeCount(); got != 1 {
		t.Fatalf("revoked %d times, want 1", got)
	}
	if status, obj, _ := penny.do("GET", "/api/v1/lichess/play-status", nil); status != 200 || obj["canPlay"] == true {
		t.Errorf("still claims play access after unlink: %v", obj)
	}
}

// A failed exchange must not half-link the student.
func TestOAuthCallbackFailureLeavesNoGrant(t *testing.T) {
	base, _ := newOAuthServer(t)
	penny := asStudent(t, base, "penny@jca.ac.th")
	state, _ := startFlow(t, penny, nil)
	completeCallback(t, base, state, "bad-code")

	if status, obj, _ := penny.do("GET", "/api/v1/lichess/play-status", nil); status != 200 || obj["canPlay"] == true {
		t.Errorf("a refused exchange granted play access: %v", obj)
	}
}

func completeCallback(t *testing.T, base *client, state, code string) int {
	t.Helper()
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	res, err := noRedirect.Get(base.srv.URL + "/api/v1/lichess/oauth/callback?code=" + url.QueryEscape(code) +
		"&state=" + url.QueryEscape(state))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	return res.StatusCode
}
