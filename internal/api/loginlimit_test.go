package api_test

import (
	"testing"

	"github.com/Kusk24/jtrax-backend/internal/db"
)

// The bug this fixes, stated as a test.
//
// The portals call this API from **their own servers** — a Next.js server
// action on Vercel, not the browser — so every member of staff arrives from one
// address. When sign-in was rate-limited per IP, the first ten attempts of the
// minute consumed the budget for the entire academy, and the eleventh person to
// try their own correct password was refused. The console showed that refusal
// as "invalid email or password", so it read as a wrong password.
func TestOneAccountsFailuresDoNotLockOutAnother(t *testing.T) {
	srv := newServer(t)

	// Somebody hammers one account well past the per-account budget.
	victim := &client{t: t, srv: srv}
	for i := 0; i < 15; i++ {
		victim.do("POST", "/api/v1/auth/login", map[string]string{
			"email": "admin@jca.ac.th", "password": "definitely-wrong",
		})
	}

	// A colleague — same source address, different account — must still sign in.
	other := &client{t: t, srv: srv}
	status, obj, _ := other.do("POST", "/api/v1/auth/login", map[string]string{
		"email": "penny@jca.ac.th", "password": db.DevPassword,
	})
	if status != 200 {
		t.Fatalf("a colleague was locked out by someone else's failures: status %d (%v)", status, obj)
	}
}

// Guessing at one account is still throttled, and says so distinctly — "wait a
// minute" and "wrong password" send a person to different places.
func TestRepeatedFailuresOnOneAccountAreThrottled(t *testing.T) {
	c := &client{t: t, srv: newServer(t)}
	sawTooMany := false
	for i := 0; i < 15; i++ {
		status, _, _ := c.do("POST", "/api/v1/auth/login", map[string]string{
			"email": "admin@jca.ac.th", "password": "definitely-wrong",
		})
		if status == 429 {
			sawTooMany = true
			break
		}
	}
	if !sawTooMany {
		t.Fatal("guessing at one account was never throttled")
	}
}

// A member of staff who fumbles their password and then gets it right should
// not still be carrying those fumbles for the rest of the minute.
func TestASuccessfulSignInClearsEarlierFailures(t *testing.T) {
	srv := newServer(t)
	c := &client{t: t, srv: srv}
	for i := 0; i < 5; i++ {
		c.do("POST", "/api/v1/auth/login", map[string]string{
			"email": "admin@jca.ac.th", "password": "wrong",
		})
	}
	if status, obj, _ := c.do("POST", "/api/v1/auth/login", map[string]string{
		"email": "admin@jca.ac.th", "password": db.DevPassword,
	}); status != 200 {
		t.Fatalf("correct password refused after fumbles: %d (%v)", status, obj)
	}
	// Five more failures must not tip it over, because the success reset it.
	for i := 0; i < 5; i++ {
		c.do("POST", "/api/v1/auth/login", map[string]string{
			"email": "admin@jca.ac.th", "password": "wrong",
		})
	}
	if status, _, _ := c.do("POST", "/api/v1/auth/login", map[string]string{
		"email": "admin@jca.ac.th", "password": db.DevPassword,
	}); status != 200 {
		t.Errorf("the budget was not reset by the successful sign-in: %d", status)
	}
}

// An address stored with capitals — from an import, a direct SQL edit, or an
// older build — must still be able to sign in, because sign-in lower-cases what
// the user types.
func TestSignInIsCaseInsensitive(t *testing.T) {
	c := &client{t: t, srv: newServer(t)}
	for _, typed := range []string{"admin@jca.ac.th", "Admin@JCA.ac.th", "  ADMIN@JCA.AC.TH  "} {
		status, obj, _ := c.do("POST", "/api/v1/auth/login", map[string]string{
			"email": typed, "password": db.DevPassword,
		})
		if status != 200 {
			t.Errorf("sign-in with %q: status %d (%v)", typed, status, obj)
		}
	}
}

/* ---- what the account endpoints now refuse ---- */

func TestAccountsRejectMalformedEmails(t *testing.T) {
	c := &client{t: t, srv: newServer(t)}
	c.login("admin@jca.ac.th")
	for _, bad := range []string{"head", "head@office", "head@", "@jca.ac.th", "head jca@ac.th"} {
		status, _, _ := c.do("POST", "/api/v1/user-accounts", map[string]string{
			"email": bad, "password": "chess1234", "role": "Receptionist", "display_name": "Desk",
		})
		if status != 400 {
			t.Errorf("account created with email %q: status %d", bad, status)
		}
	}
}

func TestAccountsEnforceThePasswordRule(t *testing.T) {
	c := &client{t: t, srv: newServer(t)}
	c.login("admin@jca.ac.th")
	for _, tc := range []struct{ name, password string }{
		{"too short", "chess1"},
		{"letters only", "chessboard"},
		{"digits only", "12345678"},
		// Three Thai characters is ten bytes: a byte-based rule let this in.
		{"three Thai characters", "กขค1"},
	} {
		status, _, _ := c.do("POST", "/api/v1/user-accounts", map[string]string{
			"email": "desk-" + tc.name + "@jca.ac.th", "password": tc.password,
			"role": "Receptionist", "display_name": "Desk",
		})
		if status != 400 {
			t.Errorf("%s (%q) was accepted: status %d", tc.name, tc.password, status)
		}
	}
	// And a compliant one goes through.
	if status, obj, _ := c.do("POST", "/api/v1/user-accounts", map[string]string{
		"email": "desk@jca.ac.th", "password": "chess1234",
		"role": "Receptionist", "display_name": "Desk",
	}); status != 201 {
		t.Errorf("a valid account was refused: %d (%v)", status, obj)
	}
}

// An account created with a capitalised address must be stored lower-cased, or
// it is created and immediately unreachable.
func TestCreatedAccountsAreStoredLowerCased(t *testing.T) {
	c := &client{t: t, srv: newServer(t)}
	c.login("admin@jca.ac.th")
	status, obj, _ := c.do("POST", "/api/v1/user-accounts", map[string]string{
		"email": "  Front.Desk@JCA.AC.TH ", "password": "chess1234",
		"role": "Receptionist", "display_name": "Front Desk",
	})
	if status != 201 {
		t.Fatalf("create: %d (%v)", status, obj)
	}
	if obj["email"] != "front.desk@jca.ac.th" {
		t.Errorf("stored as %v, want front.desk@jca.ac.th", obj["email"])
	}
	// The real check: it can actually sign in.
	fresh := &client{t: t, srv: c.srv}
	if status, _, _ := fresh.do("POST", "/api/v1/auth/login", map[string]string{
		"email": "front.desk@jca.ac.th", "password": "chess1234",
	}); status != 200 {
		t.Errorf("the account just created cannot sign in: %d", status)
	}
}
