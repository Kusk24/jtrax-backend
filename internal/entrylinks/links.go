// Payment links and codes for public tournament entries.
package entrylinks

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"net/url"
	"strings"
)

// NewPayCode returns a fresh random code and the hash to store for it. 32
// random bytes: nobody guesses that, through a rate limit or otherwise. The
// arrival reminder's codes are still made this way; pay codes are derived
// (entryPayCode).
func NewPayCode() (code, hash string, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", err
	}
	code = hex.EncodeToString(raw)
	return code, HashPayCode(code), nil
}

// EntryPayCode is the pay code for one entry, and the hash to store for it.
//
// It is an HMAC of the entry id under a key kept in server_secret, a table no
// endpoint reads, rather than random bytes: every email about an unpaid entry
// — the confirmation, the reminder, "your payment was not completed" — has to
// carry the same working link, and only the hash is stored. Without the key
// the code is as unguessable as 32 random bytes; the stored hash still opens
// nothing on its own.
func EntryPayCode(d *sql.DB, regID string) (code, hash string, err error) {
	key, err := serverSecret(d, "pay_code_key")
	if err != nil {
		return "", "", err
	}
	m := hmac.New(sha256.New, key)
	m.Write([]byte("pay:" + regID))
	code = hex.EncodeToString(m.Sum(nil))
	return code, HashPayCode(code), nil
}

// serverSecret reads a named 32-byte key, making it on first use. Two servers
// racing to make it both read back the one that won.
func serverSecret(d *sql.DB, name string) ([]byte, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	if _, err := d.Exec(`INSERT OR IGNORE INTO server_secret (name, value) VALUES (?, ?)`,
		name, hex.EncodeToString(raw)); err != nil {
		return nil, err
	}
	var v string
	if err := d.QueryRow(`SELECT value FROM server_secret WHERE name = ?`, name).Scan(&v); err != nil {
		return nil, err
	}
	return hex.DecodeString(v)
}

// CurrentPayCode is the code an email about an existing entry should carry.
// An entry made before codes were derived holds the hash of a random one;
// it is moved to the derived code here, which retires the link in its first
// email — the one cost of the change, paid once per old entry.
func CurrentPayCode(d *sql.DB, regID string) (string, error) {
	code, hash, err := EntryPayCode(d, regID)
	if err != nil {
		return "", err
	}
	if _, err := d.Exec(`UPDATE tournament_registration SET pay_code_hash = ?
	                      WHERE tournament_registration_id = ? AND COALESCE(pay_code_hash,'') <> ?`,
		hash, regID, hash); err != nil {
		return "", err
	}
	return code, nil
}

func HashPayCode(code string) string {
	sum := sha256.Sum256([]byte(code))
	return hex.EncodeToString(sum[:])
}

// PayLink is the page the confirmation email points at. The code goes after
// the #, which a browser never sends to a server, so it stays out of the web
// host's request logs. The page reads it from there and posts it in a body.
func PayLink(appURL, tournamentID, regID, code string) string {
	return strings.TrimSuffix(appURL, "/") + "/register/" + url.PathEscape(tournamentID) +
		"/pay?entry=" + url.QueryEscape(regID) + "#code=" + code
}
