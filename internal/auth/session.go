package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"
)

// A session ends after SessionIdle without use, and never outlives SessionMax
// from sign-in however busy it is. expires_at holds the idle deadline and
// slides forward on use; created_at anchors the cap.
//
// The idle window is what limits a token left on a shared or lost device; the
// cap is what limits a token that was copied and is being kept alive.
const (
	SessionIdle = 14 * 24 * time.Hour
	SessionMax  = 90 * 24 * time.Hour
)

// sessionSlideEvery throttles the slide to one write per session per hour, so
// a busy portal does not turn every read into a database write.
const sessionSlideEvery = time.Hour

// sqliteTime is the layout of datetime('now'), which fills created_at.
const sqliteTime = "2006-01-02 15:04:05"

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

// hashSessionToken is what auth_session stores in place of the token. The token
// itself exists only on the caller's device; the table holds its SHA-256, so a
// copy of the database signs nobody in. A plain hash rather than a slow one is
// enough because the token is 256 random bits, not a password somebody chose.
func hashSessionToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// CreateSession stores a fresh random token for the account and returns it.
// Only its hash is written; the returned token is the one copy.
func CreateSession(d *sql.DB, userAccountID string) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := hex.EncodeToString(raw)
	expires := time.Now().UTC().Add(SessionIdle).Format(time.RFC3339)
	_, err := d.Exec(`INSERT INTO auth_session (token_hash, user_account_id, expires_at) VALUES (?,?,?)`,
		hashSessionToken(token), userAccountID, expires)
	return token, err
}

// DeleteSession revokes a token; deleting an unknown token is not an error.
func DeleteSession(d *sql.DB, token string) error {
	_, err := d.Exec(`DELETE FROM auth_session WHERE token_hash = ?`, hashSessionToken(token))
	return err
}

var ErrNoSession = errors.New("no valid session")

// ErrArchived is a student, or a guardian, who has left the academy: their
// account stays, but it no longer signs in.
var ErrArchived = errors.New("this account is archived")

// IsArchived says whether the account belongs to an archived student or an
// archived guardian.
func IsArchived(d *sql.DB, userAccountID string) bool {
	var archived bool
	d.QueryRow(`SELECT EXISTS (SELECT 1 FROM student WHERE user_account_id = ? AND COALESCE(archived_at, '') <> '')
	                OR EXISTS (SELECT 1 FROM parent WHERE user_account_id = ? AND COALESCE(archived_at, '') <> '')`,
		userAccountID, userAccountID).Scan(&archived)
	return archived
}

// Lookup resolves a bearer token to the caller's Identity.
func Lookup(d *sql.DB, token string) (*Identity, error) {
	if token == "" {
		return nil, ErrNoSession
	}
	var id Identity
	var created, expires string
	tokenHash := hashSessionToken(token)
	err := d.QueryRow(`
		SELECT u.user_account_id, u.email, u.role, u.display_name,
		       u.language_preference, u.theme_preference, s.created_at, s.expires_at
		FROM auth_session s JOIN user_account u ON u.user_account_id = s.user_account_id
		WHERE s.token_hash = ?`, tokenHash).
		Scan(&id.UserAccountID, &id.Email, &id.Role, &id.DisplayName, &id.Language, &id.Theme, &created, &expires)
	if err == sql.ErrNoRows {
		return nil, ErrNoSession
	}
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	exp, err := time.Parse(time.RFC3339, expires)
	if err != nil || now.After(exp) {
		return nil, ErrNoSession
	}
	born, err := time.Parse(sqliteTime, created)
	if err != nil || now.After(born.Add(SessionMax)) {
		return nil, ErrNoSession
	}
	slideSession(d, tokenHash, now, exp, born)
	// Archived after signing in: the session ends with the archive.
	if (id.Role == "Student" || id.Role == "Parent") && IsArchived(d, id.UserAccountID) {
		return nil, ErrNoSession
	}
	// Attach the role-row id; each query misses harmlessly for other roles.
	d.QueryRow(`SELECT parent_id FROM parent WHERE user_account_id = ?`, id.UserAccountID).Scan(&id.ParentID)
	d.QueryRow(`SELECT teacher_id FROM teacher WHERE user_account_id = ?`, id.UserAccountID).Scan(&id.TeacherID)
	d.QueryRow(`SELECT student_id FROM student WHERE user_account_id = ?`, id.UserAccountID).Scan(&id.StudentID)
	d.QueryRow(`SELECT admin_id FROM admin WHERE user_account_id = ?`, id.UserAccountID).Scan(&id.AdminID)
	return &id, nil
}

// slideSession moves the idle deadline to now+SessionIdle, clamped to the cap,
// once at least sessionSlideEvery has passed since the last move. A failed
// write is ignored: the session is still valid now, and the next request
// tries again.
func slideSession(d *sql.DB, tokenHash string, now, exp, born time.Time) {
	next := now.Add(SessionIdle)
	if limit := born.Add(SessionMax); next.After(limit) {
		next = limit
	}
	if next.Sub(exp) < sessionSlideEvery {
		return
	}
	d.Exec(`UPDATE auth_session SET expires_at = ? WHERE token_hash = ?`, next.Format(time.RFC3339), tokenHash)
}
