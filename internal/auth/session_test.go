package auth_test

/* How long a session lasts: it slides while in use, ends when left idle, and
   never outlives the cap from sign-in. Times are moved in the table rather
   than waited for. */

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/Kusk24/jtrax-backend/internal/auth"
	"github.com/Kusk24/jtrax-backend/internal/db"
)

func sessionDB(t *testing.T) *sql.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if err := db.Seed(d, db.DevPassword); err != nil {
		t.Fatal(err)
	}
	return d
}

// age sets the session's sign-in time and idle deadline relative to now.
func age(t *testing.T, d *sql.DB, created, expires time.Duration) {
	t.Helper()
	now := time.Now().UTC()
	if _, err := d.Exec(`UPDATE auth_session SET created_at = ?, expires_at = ?`,
		now.Add(created).Format("2006-01-02 15:04:05"), now.Add(expires).Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
}

func expiresAt(t *testing.T, d *sql.DB) time.Time {
	t.Helper()
	var s string
	d.QueryRow(`SELECT expires_at FROM auth_session`).Scan(&s)
	exp, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return exp
}

func TestASessionInUseSlidesForward(t *testing.T) {
	d := sessionDB(t)
	token, _ := auth.CreateSession(d, "usr_admin1")
	age(t, d, -10*24*time.Hour, 2*24*time.Hour)

	if _, err := auth.Lookup(d, token); err != nil {
		t.Fatalf("a live session was refused: %v", err)
	}
	if left := time.Until(expiresAt(t, d)); left < auth.SessionIdle-time.Minute {
		t.Fatalf("use did not slide the deadline: %v left", left)
	}
}

func TestAnIdleSessionEnds(t *testing.T) {
	d := sessionDB(t)
	token, _ := auth.CreateSession(d, "usr_admin1")
	age(t, d, -20*24*time.Hour, -time.Minute)

	if _, err := auth.Lookup(d, token); err != auth.ErrNoSession {
		t.Fatalf("an idle session still works: %v", err)
	}
}

func TestABusySessionStillEndsAtTheCap(t *testing.T) {
	d := sessionDB(t)
	token, _ := auth.CreateSession(d, "usr_admin1")

	// Near the cap, sliding stops at the cap rather than past it.
	age(t, d, -(auth.SessionMax - 24*time.Hour), time.Hour)
	if _, err := auth.Lookup(d, token); err != nil {
		t.Fatalf("a session inside the cap was refused: %v", err)
	}
	if left := time.Until(expiresAt(t, d)); left > 24*time.Hour {
		t.Fatalf("the deadline slid past the cap: %v left", left)
	}

	// Past the cap, a deadline still in the future does not save it.
	age(t, d, -(auth.SessionMax + time.Minute), 24*time.Hour)
	if _, err := auth.Lookup(d, token); err != auth.ErrNoSession {
		t.Fatalf("a session past the cap still works: %v", err)
	}
}

func TestSlidingWritesAtMostHourly(t *testing.T) {
	d := sessionDB(t)
	token, _ := auth.CreateSession(d, "usr_admin1")
	before := expiresAt(t, d)
	if _, err := auth.Lookup(d, token); err != nil {
		t.Fatal(err)
	}
	if !expiresAt(t, d).Equal(before) {
		t.Fatal("a fresh session was rewritten on its first use")
	}
}
