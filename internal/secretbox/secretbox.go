// Package secretbox seals short secrets that have to live in the database.
//
// It exists for one reason: the LINE channel credentials are typed into the
// console by an admin at runtime, so unlike SMTP they cannot come from the
// environment. Storing them in clear would mean a copy of the database is a
// working access token — and copies of the database are ordinary: a Turso
// snapshot, a .db pulled down to reproduce a bug, a backup on a laptop.
//
// The house rule that secrets come from the environment still holds; what the
// environment holds is the key, and the database holds only ciphertext.
package secretbox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
)

// ErrNoKey means the environment has no sealing key. Callers treat this as
// "the feature is not configured" rather than as a reason to store plaintext:
// there is no degraded mode here, because the degraded mode is a leak.
var ErrNoKey = errors.New("secretbox: no sealing key configured")

// KeySize is the AES-256 key length, in bytes.
const KeySize = 32

// Box seals and opens values under one key.
type Box struct{ aead cipher.AEAD }

// FromEnv builds a Box from the named variable, which must hold 32 bytes as
// base64 or hex. Generate one with `openssl rand -base64 32`.
//
// The error never contains the key material, so it is safe to log.
func FromEnv(name string) (*Box, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return nil, ErrNoKey
	}
	key, err := decodeKey(raw)
	if err != nil {
		return nil, fmt.Errorf("secretbox: %s is not a valid key: %w", name, err)
	}
	return New(key)
}

// New builds a Box from raw key bytes.
func New(key []byte) (*Box, error) {
	if len(key) != KeySize {
		return nil, fmt.Errorf("secretbox: key must be %d bytes, got %d", KeySize, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead}, nil
}

// decodeKey accepts base64 or hex so that whichever way an operator generated
// the key, pasting it into the environment works.
func decodeKey(raw string) ([]byte, error) {
	if b, err := base64.StdEncoding.DecodeString(raw); err == nil && len(b) == KeySize {
		return b, nil
	}
	if b, err := hex.DecodeString(raw); err == nil && len(b) == KeySize {
		return b, nil
	}
	return nil, fmt.Errorf("want %d bytes as base64 or hex", KeySize)
}

// Seal encrypts plaintext, returning base64 of nonce+ciphertext.
//
// The nonce is random per call, so sealing the same token twice gives two
// different blobs — someone reading the table cannot tell that two rows hold
// the same secret.
func (b *Box) Seal(plaintext string) (string, error) {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := b.aead.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(sealed), nil
}

// Open reverses Seal. A blob sealed under a different key fails here rather
// than returning garbage — that is GCM's authentication tag doing its job, and
// it is what turns "someone rotated LINE_TOKEN_KEY" into a clear error instead
// of a confusing 401 from LINE.
func (b *Box) Open(sealed string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(sealed)
	if err != nil {
		return "", errors.New("secretbox: stored value is not valid base64")
	}
	n := b.aead.NonceSize()
	if len(raw) < n {
		return "", errors.New("secretbox: stored value is too short")
	}
	out, err := b.aead.Open(nil, raw[:n], raw[n:], nil)
	if err != nil {
		return "", errors.New("secretbox: stored value could not be decrypted — was the sealing key changed?")
	}
	return string(out), nil
}
