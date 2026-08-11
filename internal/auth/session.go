package auth

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"
)

const sessionTTL = 30 * 24 * time.Hour

// Identity is the resolved caller: the account plus its role-specific row id.
type Identity struct {
	UserAccountID string `json:"userAccountId"`
	Email         string `json:"email"`
	Role          string `json:"role"`
	DisplayName   string `json:"displayName"`
	Language      string `json:"languagePreference"`
	Theme         string `json:"themePreference"`
	// Exactly one of these is set for role-bound accounts.
	ParentID  string `json:"parentId,omitempty"`
	TeacherID string `json:"teacherId,omitempty"`
	StudentID string `json:"studentId,omitempty"`
	AdminID   string `json:"adminId,omitempty"`
}

// CreateSession stores a fresh random token for the account and returns it.
func CreateSession(d *sql.DB, userAccountID string) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := hex.EncodeToString(raw)
	expires := time.Now().UTC().Add(sessionTTL).Format(time.RFC3339)
	_, err := d.Exec(`INSERT INTO auth_session (token, user_account_id, expires_at) VALUES (?,?,?)`,
		token, userAccountID, expires)
	return token, err
}

// DeleteSession revokes a token; deleting an unknown token is not an error.
func DeleteSession(d *sql.DB, token string) error {
	_, err := d.Exec(`DELETE FROM auth_session WHERE token = ?`, token)
	return err
}

var ErrNoSession = errors.New("no valid session")

// Lookup resolves a bearer token to the caller's Identity.
func Lookup(d *sql.DB, token string) (*Identity, error) {
	if token == "" {
		return nil, ErrNoSession
	}
	var id Identity
	var expires string
	err := d.QueryRow(`
		SELECT u.user_account_id, u.email, u.role, u.display_name,
		       u.language_preference, u.theme_preference, s.expires_at
		FROM auth_session s JOIN user_account u ON u.user_account_id = s.user_account_id
		WHERE s.token = ?`, token).
		Scan(&id.UserAccountID, &id.Email, &id.Role, &id.DisplayName, &id.Language, &id.Theme, &expires)
	if err == sql.ErrNoRows {
		return nil, ErrNoSession
	}
	if err != nil {
		return nil, err
	}
	if exp, err := time.Parse(time.RFC3339, expires); err != nil || time.Now().After(exp) {
		return nil, ErrNoSession
	}
	// Attach the role-row id; each query misses harmlessly for other roles.
	d.QueryRow(`SELECT parent_id FROM parent WHERE user_account_id = ?`, id.UserAccountID).Scan(&id.ParentID)
	d.QueryRow(`SELECT teacher_id FROM teacher WHERE user_account_id = ?`, id.UserAccountID).Scan(&id.TeacherID)
	d.QueryRow(`SELECT student_id FROM student WHERE user_account_id = ?`, id.UserAccountID).Scan(&id.StudentID)
	d.QueryRow(`SELECT admin_id FROM admin WHERE user_account_id = ?`, id.UserAccountID).Scan(&id.AdminID)
	return &id, nil
}
