package db_test

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/Kusk24/jtrax-backend/internal/auth"
	"github.com/Kusk24/jtrax-backend/internal/db"
)

func TestParseStaff(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		want    int
		wantErr bool
	}{
		{name: "empty is not an error", raw: "  "},
		{
			name: "one account",
			raw:  `[{"email":"A@JCA.ac.th","password":"long-enough1","role":"Admin","name":"Head Office"}]`,
			want: 1,
		},
		{
			name: "two accounts",
			raw: `[{"email":"a@jca.ac.th","password":"long-enough1","role":"Admin","name":"A"},
			       {"email":"b@jca.ac.th","password":"long-enough1","role":"Receptionist","name":"B"}]`,
			want: 2,
		},
		{name: "not json", raw: `a@jca.ac.th:pw`, wantErr: true},
		{name: "not an array", raw: `{"email":"a@jca.ac.th"}`, wantErr: true},
		{
			name:    "unknown role",
			raw:     `[{"email":"a@jca.ac.th","password":"long-enough1","role":"Teacher","name":"A"}]`,
			wantErr: true,
		},
		{
			name:    "short password",
			raw:     `[{"email":"a@jca.ac.th","password":"short","role":"Admin","name":"A"}]`,
			wantErr: true,
		},
		{
			name:    "missing name",
			raw:     `[{"email":"a@jca.ac.th","password":"long-enough1","role":"Admin"}]`,
			wantErr: true,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := db.ParseStaff(c.raw)
			if (err != nil) != c.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, c.wantErr)
			}
			if !c.wantErr && len(got) != c.want {
				t.Fatalf("parsed %d accounts, want %d", len(got), c.want)
			}
		})
	}
}

func TestParseStaffLowercasesEmail(t *testing.T) {
	got, err := db.ParseStaff(`[{"email":" Front@JCA.ac.th ","password":"long-enough1","role":"Receptionist","name":"Front Desk"}]`)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Email != "front@jca.ac.th" {
		t.Errorf("email = %q, want it trimmed and lowercased", got[0].Email)
	}
}

// open gives each test its own migrated database.
func open(t *testing.T) *sql.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func TestEnsureStaffCreatesAnAccountThatCanSignIn(t *testing.T) {
	d := open(t)
	accounts := []db.StaffAccount{
		{Email: "head@jca.ac.th", Password: "a-real-password", Role: "Admin", Name: "JCA Head Office", Phone: "02-123-4567"},
		{Email: "front@jca.ac.th", Password: "another-password", Role: "Receptionist", Name: "Front Desk"},
	}
	written, err := db.EnsureStaff(d, accounts)
	if err != nil {
		t.Fatal(err)
	}
	if len(written) != 2 {
		t.Fatalf("wrote %d accounts, want 2", len(written))
	}

	for _, a := range accounts {
		var hash, role, name string
		if err := d.QueryRow(`SELECT password_hash, role, display_name FROM user_account WHERE email = ?`,
			a.Email).Scan(&hash, &role, &name); err != nil {
			t.Fatalf("%s: %v", a.Email, err)
		}
		if !auth.VerifyPassword(a.Password, hash) {
			t.Errorf("%s: stored hash does not verify its password", a.Email)
		}
		if role != a.Role || name != a.Name {
			t.Errorf("%s: role/name = %q/%q, want %q/%q", a.Email, role, name, a.Role, a.Name)
		}

		// The console lists staff from the admin table, so both roles need a row.
		var n int
		if err := d.QueryRow(`SELECT COUNT(*) FROM admin a JOIN user_account u
			ON u.user_account_id = a.user_account_id WHERE u.email = ?`, a.Email).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("%s: %d admin rows, want 1", a.Email, n)
		}
	}
}

func TestEnsureStaffIsIdempotentAndResetsThePassword(t *testing.T) {
	d := open(t)
	first := []db.StaffAccount{{Email: "head@jca.ac.th", Password: "first-password", Role: "Admin", Name: "Head Office"}}
	if _, err := db.EnsureStaff(d, first); err != nil {
		t.Fatal(err)
	}

	var userID string
	if err := d.QueryRow(`SELECT user_account_id FROM user_account WHERE email = ?`, "head@jca.ac.th").Scan(&userID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`INSERT INTO auth_session (token, user_account_id, expires_at) VALUES (?,?,?)`,
		"tok", userID, "2099-01-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}

	second := []db.StaffAccount{{Email: "head@jca.ac.th", Password: "second-password", Role: "Receptionist", Name: "Head Office"}}
	if _, err := db.EnsureStaff(d, second); err != nil {
		t.Fatal(err)
	}

	var accounts, admins, sessions int
	d.QueryRow(`SELECT COUNT(*) FROM user_account`).Scan(&accounts)
	d.QueryRow(`SELECT COUNT(*) FROM admin`).Scan(&admins)
	d.QueryRow(`SELECT COUNT(*) FROM auth_session WHERE user_account_id = ?`, userID).Scan(&sessions)
	if accounts != 1 || admins != 1 {
		t.Errorf("re-running duplicated rows: %d accounts, %d admins", accounts, admins)
	}
	if sessions != 0 {
		t.Errorf("%d sessions survived the password change, want 0", sessions)
	}

	var hash, role string
	d.QueryRow(`SELECT password_hash, role FROM user_account WHERE email = ?`, "head@jca.ac.th").Scan(&hash, &role)
	if auth.VerifyPassword("first-password", hash) {
		t.Error("the old password still works after a reset")
	}
	if !auth.VerifyPassword("second-password", hash) {
		t.Error("the new password does not work after a reset")
	}
	if role != "Receptionist" {
		t.Errorf("role = %q, want the updated Receptionist", role)
	}
}
