package api_test

/* A session row holds the SHA-256 of the bearer token, never the token: a copy
   of the database must not be a way to act as the people signed in. */

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestASessionIsStoredAsItsHashNotItsToken(t *testing.T) {
	d := newDB(t)
	c := &client{t: t, srv: newServerOn(t, d)}
	c.login("sandy01234@gmail.com")
	if len(c.token) != 64 {
		t.Fatalf("want a 64-character token, got %q", c.token)
	}

	var stored int
	d.QueryRow(`SELECT COUNT(*) FROM auth_session WHERE token_hash = ?`, c.token).Scan(&stored)
	if stored != 0 {
		t.Fatal("the bearer token itself is in the session table")
	}
	sum := sha256.Sum256([]byte(c.token))
	d.QueryRow(`SELECT COUNT(*) FROM auth_session WHERE token_hash = ?`, hex.EncodeToString(sum[:])).Scan(&stored)
	if stored != 1 {
		t.Fatalf("want the token's hash stored once, found %d", stored)
	}

	// The token still signs the caller in.
	if status, me, _ := c.do("GET", "/api/v1/auth/me", nil); status != 200 || me["email"] != "sandy01234@gmail.com" {
		t.Fatalf("me with the token: %d %v", status, me)
	}

	// The hash is not a credential: presented as a token, it opens nothing.
	thief := &client{t: t, srv: c.srv, token: hex.EncodeToString(sum[:])}
	if status, _, _ := thief.do("GET", "/api/v1/auth/me", nil); status != 401 {
		t.Fatalf("the stored hash worked as a token: %d", status)
	}

	// Signing out removes the row.
	if status, _, _ := c.do("POST", "/api/v1/auth/logout", nil); status != 200 && status != 204 {
		t.Fatalf("logout: %d", status)
	}
	d.QueryRow(`SELECT COUNT(*) FROM auth_session`).Scan(&stored)
	if stored != 0 {
		t.Fatalf("logout left %d session rows", stored)
	}
	if status, _, _ := c.do("GET", "/api/v1/auth/me", nil); status != 401 {
		t.Fatalf("the token still works after logout: %d", status)
	}
}
