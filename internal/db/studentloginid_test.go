package db

import (
	"path/filepath"
	"testing"
)

const migration0027 = "0027_a_child_signs_in_with_an_id.sql"

// reapply0027 rewinds one migration and runs it again through the real runner,
// so the SQL under test is the SQL that will run on Turso — not a paraphrase of
// it written in Go.
func reapply0027(t *testing.T, rows [][2]string) map[string]string {
	t.Helper()
	d, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })

	for i, r := range rows {
		id, email := "usr_fixture_"+string(rune('a'+i)), r[0]
		if _, err := d.Exec(`INSERT INTO user_account
			(user_account_id, email, password_hash, role, display_name)
			VALUES (?,?,?,?,?)`, id, email, "x", r[1], "Fixture"); err != nil {
			t.Fatalf("seed %s: %v", email, err)
		}
	}

	if _, err := d.Exec(`DELETE FROM schema_migration WHERE filename = ?`, migration0027); err != nil {
		t.Fatal(err)
	}
	if err := migrate(d); err != nil {
		t.Fatalf("re-applying %s: %v", migration0027, err)
	}

	got := map[string]string{}
	/* Ordered, so row i really is the fixture at rows[i]: the ids are
	   `usr_fixture_a`, `_b`, … in insertion order, and an unordered SELECT
	   only happens to agree with that. */
	res, err := d.Query(`SELECT display_name, email FROM user_account
		WHERE user_account_id LIKE 'usr_fixture_%' ORDER BY user_account_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Close()
	for i := 0; res.Next(); i++ {
		var name, email string
		if err := res.Scan(&name, &email); err != nil {
			t.Fatal(err)
		}
		got[rows[i][0]] = email
	}
	return got
}

func TestTheInventedStudentMailboxBecomesAnID(t *testing.T) {
	got := reapply0027(t, [][2]string{
		{"penny.ward@student.jca.ac.th", "Student"},
		// A real address a family gave: it can receive a reset link, and
		// taking it away removes the only self-service route the child has.
		{"olderkid@gmail.com", "Student"},
		// Not a student, not this migration's business — and the domain rule
		// is what decides, so a parent on the invented student domain would be
		// left alone too.
		{"sandy@gmail.com", "Parent"},
	})

	want := map[string]string{
		"penny.ward@student.jca.ac.th": "stu_penny_ward",
		"olderkid@gmail.com":           "olderkid@gmail.com",
		"sandy@gmail.com":              "sandy@gmail.com",
	}
	for before, after := range want {
		if got[before] != after {
			t.Errorf("%s became %q, want %q", before, got[before], after)
		}
	}
}

// A collision must make the migration decline, not fail. A failed migration is
// a failed deploy, and the whole academy is down for two rows that need a
// human to look at them.
func TestTheConversionDeclinesRatherThanCollides(t *testing.T) {
	got := reapply0027(t, [][2]string{
		// Already holds the ID the conversion below would want.
		{"stu_penny_ward", "Student"},
		{"penny.ward@student.jca.ac.th", "Student"},
		// Two addresses that flatten to one ID: `.` and `-` both become `_`,
		// so these look distinct today and would land on the same string.
		{"john.smith@student.jca.ac.th", "Student"},
		{"john-smith@student.jca.ac.th", "Student"},
	})

	for _, kept := range []string{
		"penny.ward@student.jca.ac.th",
		"john.smith@student.jca.ac.th",
		"john-smith@student.jca.ac.th",
	} {
		if got[kept] != kept {
			t.Errorf("%s was converted to %q — it should have been left for a human", kept, got[kept])
		}
	}
	if got["stu_penny_ward"] != "stu_penny_ward" {
		t.Errorf("the account already holding the ID was changed to %q", got["stu_penny_ward"])
	}
}

// Running it twice must not move anything the second time — Render re-runs the
// full set on a fresh database, and a migration that is not idempotent is one
// that behaves differently on a restore than it did in production.
func TestTheConversionIsIdempotent(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.Exec(`INSERT INTO user_account
		(user_account_id, email, password_hash, role, display_name)
		VALUES ('usr_p', 'penny.ward@student.jca.ac.th', 'x', 'Student', 'Penny')`); err != nil {
		t.Fatal(err)
	}
	for pass := 1; pass <= 2; pass++ {
		if _, err := d.Exec(`DELETE FROM schema_migration WHERE filename = ?`, migration0027); err != nil {
			t.Fatal(err)
		}
		if err := migrate(d); err != nil {
			t.Fatalf("pass %d: %v", pass, err)
		}
		var email string
		if err := d.QueryRow(`SELECT email FROM user_account WHERE user_account_id = 'usr_p'`).Scan(&email); err != nil {
			t.Fatal(err)
		}
		if email != "stu_penny_ward" {
			t.Fatalf("after pass %d the ID is %q, want stu_penny_ward", pass, email)
		}
	}
}
