package api_test

import (
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Kusk24/jtrax-backend/internal/api"
	"github.com/Kusk24/jtrax-backend/internal/auth"
	"github.com/Kusk24/jtrax-backend/internal/db"
	"github.com/Kusk24/jtrax-backend/internal/mail"
)

// captureSender stands in for SMTP so the flow can be driven end to end.
type captureSender struct {
	mu   sync.Mutex
	sent []struct{ To, Subject, Body string }
}

func (c *captureSender) Send(to, subject, body string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sent = append(c.sent, struct{ To, Subject, Body string }{to, subject, body})
	return nil
}

func (c *captureSender) last() (string, string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.sent) == 0 {
		return "", ""
	}
	m := c.sent[len(c.sent)-1]
	return m.To, m.Body
}

func newResetServer(t *testing.T) (*httptest.Server, *captureSender) {
	d, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if err := db.Seed(d, db.DevPassword); err != nil {
		t.Fatal(err)
	}
	cap := &captureSender{}
	srv := httptest.NewServer(api.NewHandlerWithMail(d, mail.Config{AppURL: "https://portal.example"}, cap))
	t.Cleanup(srv.Close)
	return srv, cap
}

// tokenFrom pulls the raw token out of the link in the mail body.
func tokenFrom(body string) string {
	_, after, found := strings.Cut(body, "token=")
	if !found {
		return ""
	}
	tok, _, _ := strings.Cut(after, "\n")
	return strings.TrimSpace(tok)
}

func TestResetPasswordEndToEnd(t *testing.T) {
	srv, cap := newResetServer(t)
	c := &client{t: t, srv: srv}

	// A session that exists before the reset, to prove it gets revoked.
	c.login("sandy01234@gmail.com")
	before := c.token
	status, _, _ := c.do("GET", "/api/v1/auth/me", nil)
	if status != 200 {
		t.Fatalf("pre-reset session should work, got %d", status)
	}

	status, _, _ = c.do("POST", "/api/v1/auth/forgot-password", map[string]string{"email": "sandy01234@gmail.com"})
	if status != 202 {
		t.Fatalf("forgot-password: want 202, got %d", status)
	}
	to, body := cap.last()
	if to != "sandy01234@gmail.com" {
		t.Fatalf("mail went to %q", to)
	}
	if !strings.Contains(body, "https://portal.example/reset-password?token=") {
		t.Fatalf("mail body has no reset link: %q", body)
	}
	token := tokenFrom(body)
	if token == "" {
		t.Fatal("no token in the link")
	}

	// The raw token must not be what is stored.
	c.token = ""
	status, _, _ = c.do("POST", "/api/v1/auth/reset-password", map[string]string{
		"token": token, "password": "a-brand-new-password",
	})
	if status != 200 {
		t.Fatalf("reset-password: want 200, got %d", status)
	}

	// Old password is dead, new one works.
	status, _, _ = c.do("POST", "/api/v1/auth/login", map[string]string{
		"email": "sandy01234@gmail.com", "password": db.DevPassword,
	})
	if status != 401 {
		t.Fatalf("old password should be rejected, got %d", status)
	}
	status, obj, _ := c.do("POST", "/api/v1/auth/login", map[string]string{
		"email": "sandy01234@gmail.com", "password": "a-brand-new-password",
	})
	if status != 200 {
		t.Fatalf("new password should work, got %d (%v)", status, obj)
	}

	// The session held before the reset must be gone.
	c.token = before
	if status, _, _ := c.do("GET", "/api/v1/auth/me", nil); status != 401 {
		t.Fatalf("pre-reset session survived the reset (got %d) — a thief with the old password keeps access", status)
	}
}

func TestResetTokenIsSingleUse(t *testing.T) {
	srv, cap := newResetServer(t)
	c := &client{t: t, srv: srv}
	c.do("POST", "/api/v1/auth/forgot-password", map[string]string{"email": "penny@jca.ac.th"})
	_, body := cap.last()
	token := tokenFrom(body)

	if status, _, _ := c.do("POST", "/api/v1/auth/reset-password", map[string]string{
		"token": token, "password": "first-new-password",
	}); status != 200 {
		t.Fatalf("first use: want 200, got %d", status)
	}
	if status, _, _ := c.do("POST", "/api/v1/auth/reset-password", map[string]string{
		"token": token, "password": "second-new-password",
	}); status != 400 {
		t.Fatalf("replayed token: want 400, got %d", status)
	}
	// The replay must not have taken effect.
	if status, _, _ := c.do("POST", "/api/v1/auth/login", map[string]string{
		"email": "penny@jca.ac.th", "password": "second-new-password",
	}); status != 401 {
		t.Fatalf("replayed reset changed the password anyway, got %d", status)
	}
}

// Requesting a second link must void the first, or "I didn't get it, send
// again" quietly leaves two live tokens behind.
func TestSecondRequestVoidsTheFirstLink(t *testing.T) {
	srv, cap := newResetServer(t)
	c := &client{t: t, srv: srv}
	c.do("POST", "/api/v1/auth/forgot-password", map[string]string{"email": "uri@jca.ac.th"})
	_, firstBody := cap.last()
	first := tokenFrom(firstBody)
	c.do("POST", "/api/v1/auth/forgot-password", map[string]string{"email": "uri@jca.ac.th"})
	_, secondBody := cap.last()
	second := tokenFrom(secondBody)
	if first == second {
		t.Fatal("two requests produced the same token")
	}
	if status, _, _ := c.do("POST", "/api/v1/auth/reset-password", map[string]string{
		"token": second, "password": "newest-password",
	}); status != 200 {
		t.Fatalf("second link should work, got %d", status)
	}
	if status, _, _ := c.do("POST", "/api/v1/auth/reset-password", map[string]string{
		"token": first, "password": "attacker-password",
	}); status != 400 {
		t.Fatalf("first link should be void, got %d", status)
	}
}

// An unknown address must be indistinguishable from a known one, or the
// endpoint becomes a way to discover who attends the academy.
func TestForgotPasswordDoesNotRevealAccounts(t *testing.T) {
	srv, cap := newResetServer(t)
	c := &client{t: t, srv: srv}

	knownStatus, knownObj, _ := c.do("POST", "/api/v1/auth/forgot-password", map[string]string{"email": "penny@jca.ac.th"})
	unknownStatus, unknownObj, _ := c.do("POST", "/api/v1/auth/forgot-password", map[string]string{"email": "nobody@example.com"})

	if knownStatus != unknownStatus {
		t.Errorf("status differs: known %d vs unknown %d", knownStatus, unknownStatus)
	}
	if knownObj["status"] != unknownObj["status"] {
		t.Errorf("body differs: known %q vs unknown %q", knownObj["status"], unknownObj["status"])
	}
	if to, _ := cap.last(); to == "nobody@example.com" {
		t.Error("sent mail to an address with no account")
	}
}

func TestResetRejectsBadInput(t *testing.T) {
	srv, cap := newResetServer(t)
	c := &client{t: t, srv: srv}
	c.do("POST", "/api/v1/auth/forgot-password", map[string]string{"email": "penny@jca.ac.th"})
	_, body := cap.last()
	token := tokenFrom(body)

	cases := []struct {
		name  string
		token string
		pass  string
	}{
		{"unknown token", "deadbeef", "long-enough-password"},
		{"empty token", "", "long-enough-password"},
		{"short password", token, "short"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if status, _, _ := c.do("POST", "/api/v1/auth/reset-password", map[string]string{
				"token": tc.token, "password": tc.pass,
			}); status != 400 {
				t.Fatalf("want 400, got %d", status)
			}
		})
	}
	// The valid token survived the short-password attempt.
	if status, _, _ := c.do("POST", "/api/v1/auth/reset-password", map[string]string{
		"token": token, "password": "long-enough-password",
	}); status != 200 {
		t.Fatalf("token should still be usable, got %d", status)
	}
}

// The stored row must be a hash: leaking this table must not grant resets.
func TestStoredTokenIsHashedNotRaw(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := db.Seed(d, db.DevPassword); err != nil {
		t.Fatal(err)
	}
	var accountID string
	if err := d.QueryRow(`SELECT user_account_id FROM user_account WHERE email = ?`, "penny@jca.ac.th").Scan(&accountID); err != nil {
		t.Fatal(err)
	}
	token, err := auth.CreateReset(d, accountID)
	if err != nil {
		t.Fatal(err)
	}
	var n int
	d.QueryRow(`SELECT COUNT(*) FROM password_reset WHERE token_hash = ?`, token).Scan(&n)
	if n != 0 {
		t.Fatal("the raw token is stored in the database")
	}
	d.QueryRow(`SELECT COUNT(*) FROM password_reset WHERE token_hash = ?`, auth.HashResetToken(token)).Scan(&n)
	if n != 1 {
		t.Fatalf("hashed token not found, got %d rows", n)
	}
}
