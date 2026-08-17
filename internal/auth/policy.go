// Account rules: what counts as an email address, and what counts as a
// password.
//
// Gathered here because they were previously four separate checks in four
// files — `len(p) < 8` written out three times and a constant the fourth — and
// rules copied by hand drift. Every path that creates or changes an account now
// calls the same two functions.
package auth

import (
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MinPasswordLength is the shortest password the academy accepts, in
// characters. The one number, in one place.
const MinPasswordLength = 8

// MaxPasswordLength is a sanity bound, not a security one. bcrypt-style hashes
// ignore bytes past a limit anyway, and an unbounded field is a way to make the
// server do expensive work on request.
const MaxPasswordLength = 200

var (
	ErrPasswordShort  = fmt.Errorf("password must be at least %d characters", MinPasswordLength)
	ErrPasswordLong   = fmt.Errorf("password must be at most %d characters", MaxPasswordLength)
	ErrPasswordSpaces = errors.New("password cannot start or end with a space")
	ErrPasswordWeak   = errors.New("password must contain a letter and a number")
	ErrEmailInvalid   = errors.New("that does not look like an email address")
)

// ValidatePassword applies the academy's password rule.
//
// Length is counted in **characters, not bytes**. `len(s) >= 8` — which is what
// this code used to do in four places — accepts a three-character Thai password,
// because Thai is three bytes per character. A rule that says "8 characters"
// and means "8 bytes" is not the rule anyone was told.
//
// The strength requirement is deliberately mild: a letter and a number. Staff
// here are receptionists and teachers, not security engineers, and a rule that
// demands punctuation and mixed case produces `Password1!` on a sticky note.
// Length does more work than complexity, which is why the minimum is the part
// that is enforced strictly.
func ValidatePassword(password string) error {
	if strings.TrimSpace(password) != password {
		// Caught before length, because a password that is trimmed on the way
		// in and not on the way out can never be typed again.
		return ErrPasswordSpaces
	}
	n := utf8.RuneCountInString(password)
	if n < MinPasswordLength {
		return ErrPasswordShort
	}
	if n > MaxPasswordLength {
		return ErrPasswordLong
	}
	var hasLetter, hasDigit bool
	for _, r := range password {
		switch {
		case unicode.IsLetter(r):
			hasLetter = true
		case unicode.IsDigit(r):
			hasDigit = true
		}
	}
	if !hasLetter || !hasDigit {
		return ErrPasswordWeak
	}
	return nil
}

// NormalizeEmail is the canonical form an address is stored and matched in.
//
// Lower-cased, because an address is case-insensitive in practice and the
// column's UNIQUE index is not: without this, `Head@jca.ac.th` and
// `head@jca.ac.th` are two accounts, and only one of them can ever sign in —
// sign-in lower-cases what the user types.
func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// ValidateEmail checks the address is one an email could actually be sent to,
// and returns its canonical form.
//
// `mail.ParseAddress` accepts things a login field should not — display names,
// angle brackets, addresses with no dot in the domain — so the result is
// checked further. The point is not RFC purity; it is that an account created
// with `head` or `head@office` can never receive a password-reset link, and
// nobody finds out until the day they need one.
func ValidateEmail(email string) (string, error) {
	normalized := NormalizeEmail(email)
	if normalized == "" || len(normalized) > 254 {
		return "", ErrEmailInvalid
	}
	addr, err := mail.ParseAddress(normalized)
	if err != nil || addr.Address != normalized {
		return "", ErrEmailInvalid
	}
	at := strings.LastIndex(normalized, "@")
	local, domain := normalized[:at], normalized[at+1:]
	if local == "" || !strings.Contains(domain, ".") ||
		strings.HasPrefix(domain, ".") || strings.HasSuffix(domain, ".") ||
		strings.Contains(normalized, " ") {
		return "", ErrEmailInvalid
	}
	return normalized, nil
}
