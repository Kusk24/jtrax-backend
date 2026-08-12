// Single-use password-reset tokens. The raw token exists only in the email we
// send; the database stores its SHA-256, so this table is not a credential.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"
)

// ResetTTL is deliberately short. The link travels through email, which we do
// not control once it leaves, so the window in which a stolen one is useful
// matters more than the inconvenience of asking for a second link.
const ResetTTL = 60 * time.Minute

var ErrResetInvalid = errors.New("reset token is invalid or has expired")

// HashResetToken is exported so tests can look up a row by token without
// duplicating the hashing rule.
func HashResetToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// CreateReset issues a token for the account and returns the raw value, which
// is the only time it exists in readable form.
func CreateReset(d *sql.DB, userAccountID string) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := hex.EncodeToString(raw)
	expires := time.Now().UTC().Add(ResetTTL).Format(time.RFC3339)
	_, err := d.Exec(`INSERT INTO password_reset (token_hash, user_account_id, expires_at) VALUES (?,?,?)`,
		HashResetToken(token), userAccountID, expires)
	if err != nil {
		return "", err
	}
	return token, nil
}

// ConsumeReset validates a token and applies the new password in one step.
//
// It runs as a transaction that marks the token used, rewrites the hash and
// deletes every session for the account. The session wipe is the point: if the
// reset was triggered because someone else had the old password, leaving their
// existing session alive would make the reset pointless.
func ConsumeReset(d *sql.DB, token, newPassword string) error {
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var userID, expires string
	var usedAt sql.NullString
	err = tx.QueryRow(`SELECT user_account_id, expires_at, used_at FROM password_reset WHERE token_hash = ?`,
		HashResetToken(token)).Scan(&userID, &expires, &usedAt)
	if err == sql.ErrNoRows {
		return ErrResetInvalid
	}
	if err != nil {
		return err
	}
	if usedAt.Valid {
		return ErrResetInvalid
	}
	if exp, err := time.Parse(time.RFC3339, expires); err != nil || time.Now().After(exp) {
		return ErrResetInvalid
	}

	hash, err := HashPassword(newPassword)
	if err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := tx.Exec(`UPDATE user_account SET password_hash = ? WHERE user_account_id = ?`, hash, userID); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE password_reset SET used_at = ? WHERE token_hash = ?`, now, HashResetToken(token)); err != nil {
		return err
	}
	// Any other outstanding link for this account is void too — otherwise a
	// second "forgot password" request left a usable token behind.
	if _, err := tx.Exec(`UPDATE password_reset SET used_at = ? WHERE user_account_id = ? AND used_at IS NULL`, now, userID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM auth_session WHERE user_account_id = ?`, userID); err != nil {
		return err
	}
	return tx.Commit()
}
