package secretbox_test

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/Kusk24/jtrax-backend/internal/secretbox"
)

func key(fill byte) []byte { return bytes.Repeat([]byte{fill}, secretbox.KeySize) }

func TestSealAndOpenRoundTrip(t *testing.T) {
	b, err := secretbox.New(key('a'))
	if err != nil {
		t.Fatal(err)
	}
	const secret = "channel-access-token-with-ธาย-characters"
	sealed, err := b.Seal(secret)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sealed, secret) {
		t.Fatalf("the plaintext is visible in the sealed value: %s", sealed)
	}
	got, err := b.Open(sealed)
	if err != nil {
		t.Fatal(err)
	}
	if got != secret {
		t.Errorf("Open = %q, want %q", got, secret)
	}
}

// A different key must fail loudly rather than return rubbish — this is what
// turns "somebody rotated LINE_TOKEN_KEY" into a clear error instead of a
// baffling 401 from LINE.
func TestOpenUnderTheWrongKeyFails(t *testing.T) {
	mine, _ := secretbox.New(key('a'))
	theirs, _ := secretbox.New(key('b'))

	sealed, err := mine.Seal("the token")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := theirs.Open(sealed); err == nil {
		t.Fatalf("a foreign key opened the value and got %q", got)
	}
}

func TestTamperedCiphertextIsRejected(t *testing.T) {
	b, _ := secretbox.New(key('a'))
	sealed, _ := b.Seal("the token")

	raw, err := base64.StdEncoding.DecodeString(sealed)
	if err != nil {
		t.Fatal(err)
	}
	raw[len(raw)-1] ^= 0xff // flip a bit in the authentication tag
	if _, err := b.Open(base64.StdEncoding.EncodeToString(raw)); err == nil {
		t.Fatal("a tampered value was accepted")
	}
}

// Sealing the same secret twice must not produce the same blob, or anyone with
// read access to the table could tell that two rows hold the same credential.
func TestSealIsNotDeterministic(t *testing.T) {
	b, _ := secretbox.New(key('a'))
	first, _ := b.Seal("same")
	second, _ := b.Seal("same")
	if first == second {
		t.Fatal("two seals of the same plaintext are identical — the nonce is not random")
	}
}

func TestKeyMustBeTheRightLength(t *testing.T) {
	if _, err := secretbox.New([]byte("too short")); err == nil {
		t.Fatal("a short key was accepted")
	}
}

func TestFromEnvAcceptsBase64AndHex(t *testing.T) {
	for _, tc := range []struct {
		name, value string
	}{
		{"base64", base64.StdEncoding.EncodeToString(key('c'))},
		{"hex", hex.EncodeToString(key('c'))},
		// Pasting from a terminal picks up whitespace surprisingly often.
		{"base64 with surrounding whitespace", "  " + base64.StdEncoding.EncodeToString(key('c')) + "\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("TEST_SEAL_KEY", tc.value)
			if _, err := secretbox.FromEnv("TEST_SEAL_KEY"); err != nil {
				t.Fatalf("FromEnv: %v", err)
			}
		})
	}
}

func TestFromEnvReportsAMissingKeyDistinctly(t *testing.T) {
	t.Setenv("TEST_SEAL_KEY", "")
	_, err := secretbox.FromEnv("TEST_SEAL_KEY")
	if !errors.Is(err, secretbox.ErrNoKey) {
		t.Fatalf("want ErrNoKey so the caller can treat it as unconfigured, got %v", err)
	}
}

// The error is logged at boot, so it must not carry the key material.
func TestFromEnvErrorDoesNotLeakTheKey(t *testing.T) {
	const bad = "this-is-not-a-valid-key-but-is-secret"
	t.Setenv("TEST_SEAL_KEY", bad)
	_, err := secretbox.FromEnv("TEST_SEAL_KEY")
	if err == nil {
		t.Fatal("a malformed key was accepted")
	}
	if strings.Contains(err.Error(), bad) {
		t.Fatalf("the error echoes the key: %v", err)
	}
}
