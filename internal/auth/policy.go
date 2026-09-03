// Account rules: what counts as a sign-in identifier, and what counts as a
// password.
//
// Gathered here because they were previously four separate checks in four
// files — `len(p) < 8` written out three times and a constant the fourth — and
// rules copied by hand drift. Every path that creates or changes an account now
// calls the same functions.
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
	ErrLoginIDInvalid = errors.New("a sign-in ID must be 3-64 characters of letters, digits, dot, dash or underscore, or a valid email address")
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

// LooksLikeEmail reports whether an identifier is an address something could
// actually be sent to — the test every feature that *mails* a person has to
// make before it tries.
func LooksLikeEmail(identifier string) bool {
	_, err := ValidateEmail(identifier)
	return err == nil
}

// ValidateLoginID checks a sign-in identifier and returns its canonical form.
//
// An account signs in with one string, and for most of the academy that string
// is an email address. It cannot be for the children: a seven-year-old has no
// mailbox, and the console used to paper over that by minting
// `penny.ward@student.jca.ac.th` — an address shaped like a promise the academy
// could not keep. Nobody can receive at it, so the reset link it exists to
// carry goes nowhere, and it invites the office to write to a child who has no
// inbox. **A fake address is worse than an honest ID**, because only one of the
// two tells you what it cannot do.
//
// So an identifier is either a real email address or a bare ID: `stu_penny_ward`.
// One column, one namespace, one UNIQUE index — an ID cannot collide with an
// address, because an address contains an `@` and an ID may not.
//
// The character set is the intersection of what reads back correctly over the
// phone and what survives a URL, a CSV and a shell without quoting. No spaces:
// an identifier a parent has to type must not have an invisible difference
// between two versions of itself.
func ValidateLoginID(identifier string) (string, error) {
	normalized := NormalizeEmail(identifier)
	if strings.Contains(normalized, "@") {
		// Anything claiming to be an address is held to the address rules, so
		// `penny@student` cannot slip through as "just an ID with an @ in it".
		return ValidateEmail(identifier)
	}
	n := len(normalized)
	if n < 3 || n > 64 {
		return "", ErrLoginIDInvalid
	}
	for i, r := range normalized {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		case (r == '.' || r == '-' || r == '_') && i > 0:
			// Not leading, so an ID always starts with something a person
			// would read aloud.
		default:
			return "", ErrLoginIDInvalid
		}
	}
	return normalized, nil
}
