// Staff sign-ins are provisioned from the environment. Seed is a one-shot demo
// fixture that refuses to touch a database anyone already uses; this is the
// ongoing way to give a deployed database a real account, or to reset a
// forgotten password, without shell access to the host.
package db

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Kusk24/jtrax-backend/internal/auth"
)

// StaffAccount is one entry of the JTRAX_STAFF list.
type StaffAccount struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	Role     string `json:"role"`
	Name     string `json:"name"`
	Phone    string `json:"phone"`
	LineID   string `json:"line_id"`
}

// ParseStaff reads JTRAX_STAFF, a JSON array of accounts. An empty value means
// no accounts and no error — the variable is optional.
func ParseStaff(raw string) ([]StaffAccount, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var list []StaffAccount
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		return nil, fmt.Errorf("JTRAX_STAFF is not a JSON array of accounts: %w", err)
	}
	for i := range list {
		a := &list[i]
		a.Email = strings.ToLower(strings.TrimSpace(a.Email))
		a.Role = strings.TrimSpace(a.Role)
		a.Name = strings.TrimSpace(a.Name)
		if a.Email == "" || a.Name == "" {
			return nil, fmt.Errorf("JTRAX_STAFF[%d]: email and name are required", i)
		}
		if a.Role != "Admin" && a.Role != "Receptionist" {
			return nil, fmt.Errorf("JTRAX_STAFF[%d] (%s): role must be Admin or Receptionist", i, a.Email)
		}
		// The console signs in with this password over a public URL, so hold it
		// to the same floor the create-account endpoint enforces.
		if len(a.Password) < 8 {
			return nil, fmt.Errorf("JTRAX_STAFF[%d] (%s): password must be at least 8 characters", i, a.Email)
		}
	}
	return list, nil
}

// EnsureStaff creates each account, or updates the password, role and profile
// of one that already exists. It returns the emails written so the caller can
// log them; passwords are never returned, logged or echoed.
func EnsureStaff(d *sql.DB, accounts []StaffAccount) ([]string, error) {
	written := make([]string, 0, len(accounts))
	for _, a := range accounts {
		hash, err := auth.HashPassword(a.Password)
		if err != nil {
			return nil, err
		}

		var userID string
		err = d.QueryRow(`SELECT user_account_id FROM user_account WHERE email = ?`, a.Email).Scan(&userID)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			userID = newID("usr")
			if _, err := d.Exec(`INSERT INTO user_account (user_account_id, email, password_hash, role, display_name)
				VALUES (?,?,?,?,?)`, userID, a.Email, hash, a.Role, a.Name); err != nil {
				return nil, fmt.Errorf("create %s: %w", a.Email, err)
			}
		case err != nil:
			return nil, fmt.Errorf("look up %s: %w", a.Email, err)
		default:
			if _, err := d.Exec(`UPDATE user_account SET password_hash = ?, role = ?, display_name = ?
				WHERE user_account_id = ?`, hash, a.Role, a.Name, userID); err != nil {
				return nil, fmt.Errorf("update %s: %w", a.Email, err)
			}
			// Changing a password ends the sessions it opened, so a reset
			// actually locks out whoever was holding the old one.
			if _, err := d.Exec(`DELETE FROM auth_session WHERE user_account_id = ?`, userID); err != nil {
				return nil, fmt.Errorf("clear sessions for %s: %w", a.Email, err)
			}
		}

		// Both staff roles need an admin row: it is what the console's Admins
		// page lists, and what carries the phone and LINE id.
		var adminID string
		err = d.QueryRow(`SELECT admin_id FROM admin WHERE user_account_id = ?`, userID).Scan(&adminID)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			if _, err := d.Exec(`INSERT INTO admin (admin_id, user_account_id, name, phone, email, line_id)
				VALUES (?,?,?,?,?,?)`, newID("adm"), userID, a.Name, a.Phone, a.Email, a.LineID); err != nil {
				return nil, fmt.Errorf("create admin row for %s: %w", a.Email, err)
			}
		case err != nil:
			return nil, fmt.Errorf("look up admin row for %s: %w", a.Email, err)
		default:
			if _, err := d.Exec(`UPDATE admin SET name = ?, phone = ?, email = ?, line_id = ?
				WHERE admin_id = ?`, a.Name, a.Phone, a.Email, a.LineID, adminID); err != nil {
				return nil, fmt.Errorf("update admin row for %s: %w", a.Email, err)
			}
		}

		written = append(written, a.Email)
	}
	return written, nil
}

func newID(prefix string) string {
	raw := make([]byte, 5)
	rand.Read(raw)
	return prefix + "_" + hex.EncodeToString(raw)
}
