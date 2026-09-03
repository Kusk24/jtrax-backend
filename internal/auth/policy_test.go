package auth_test

import (
	"strings"
	"testing"

	"github.com/Kusk24/jtrax-backend/internal/auth"
)

func TestValidatePasswordLength(t *testing.T) {
	for _, tc := range []struct {
		name, password string
		ok             bool
	}{
		{"eight with a digit", "chess123", true},
		{"seven", "chess12", false},
		{"empty", "", false},
		{"letters only", "chessboard", false},
		{"digits only", "12345678", false},
		{"long and fine", "the rooks are on the seventh rank 2026", true},
		{"leading space", " chess123", false},
		{"trailing space", "chess123 ", false},
		{"absurdly long", strings.Repeat("a1", 200), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := auth.ValidatePassword(tc.password)
			if (err == nil) != tc.ok {
				t.Errorf("ValidatePassword(%q) = %v, want ok=%v", tc.password, err, tc.ok)
			}
		})
	}
}

// The bug this rule was written to fix.
//
// Thai is three bytes per character, so `len(s) >= 8` — which is what four
// separate call sites used to do — accepts a **three-character** Thai password.
// A rule that says eight characters and means eight bytes is not the rule
// anybody was told.
func TestPasswordLengthIsCharactersNotBytes(t *testing.T) {
	const threeThaiChars = "กขค1" // 3 Thai letters + a digit: 10 bytes, 4 characters
	if len(threeThaiChars) < 8 {
		t.Fatalf("fixture is wrong: %q is %d bytes", threeThaiChars, len(threeThaiChars))
	}
	if err := auth.ValidatePassword(threeThaiChars); err == nil {
		t.Errorf("%q is only 4 characters and must be rejected — a byte count would have let it through", threeThaiChars)
	}

	// And a genuinely long Thai password is accepted, so the rule is not just
	// "no Thai allowed".
	const eightThai = "กขคงจฉชญ7"
	if err := auth.ValidatePassword(eightThai); err != nil {
		t.Errorf("ValidatePassword(%q) = %v, want accepted", eightThai, err)
	}
}

func TestValidateEmail(t *testing.T) {
	for _, tc := range []struct {
		name, in, want string
	}{
		{"plain", "head@jca.ac.th", "head@jca.ac.th"},
		{"upper-cased", "Head@JCA.ac.th", "head@jca.ac.th"},
		{"padded", "  head@jca.ac.th \n", "head@jca.ac.th"},
		{"plus addressing", "head+tournaments@jca.ac.th", "head+tournaments@jca.ac.th"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := auth.ValidateEmail(tc.in)
			if err != nil {
				t.Fatalf("ValidateEmail(%q) = %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("ValidateEmail(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}

	// Each of these creates an account that can never be sent a password-reset
	// link, and nobody finds out until the day somebody needs one.
	for _, bad := range []string{
		"", "head", "head@", "@jca.ac.th", "head@office", "head jca@ac.th",
		"head@@jca.ac.th", "head@.th", "head@jca.", "Head Office <head@jca.ac.th>",
		strings.Repeat("a", 250) + "@jca.ac.th",
	} {
		if _, err := auth.ValidateEmail(bad); err == nil {
			t.Errorf("ValidateEmail(%q) was accepted", bad)
		}
	}
}

// Sign-in lower-cases what is typed, so anything stored must be lower-cased the
// same way or the account is unreachable.
func TestNormalizeEmailMatchesWhatSignInLooksUp(t *testing.T) {
	for _, in := range []string{"Head@JCA.ac.th", " head@jca.ac.th ", "HEAD@JCA.AC.TH"} {
		if got := auth.NormalizeEmail(in); got != "head@jca.ac.th" {
			t.Errorf("NormalizeEmail(%q) = %q", in, got)
		}
	}
}

func TestValidateLoginID(t *testing.T) {
	for _, tc := range []struct {
		name, in, want string
	}{
		{"a child's id", "stu_penny_ward", "stu_penny_ward"},
		{"upper-cased", "STU_Penny_Ward", "stu_penny_ward"},
		{"padded", "  stu_penny_ward \n", "stu_penny_ward"},
		{"disambiguated", "stu_john_smith_2", "stu_john_smith_2"},
		{"dots and dashes", "stu.penny-ward", "stu.penny-ward"},
		{"shortest allowed", "abc", "abc"},
		// An address is still an identifier: everyone who has a mailbox keeps
		// signing in with it, and the same function has to accept both.
		{"an address", "Head@JCA.ac.th", "head@jca.ac.th"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := auth.ValidateLoginID(tc.in)
			if err != nil {
				t.Fatalf("ValidateLoginID(%q) = %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("ValidateLoginID(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}

	for _, tc := range []struct{ name, in string }{
		{"empty", ""},
		{"two characters", "ab"},
		{"a space", "stu penny"},
		{"leading dot", ".penny"},
		{"leading underscore", "_penny"},
		{"punctuation", "stu_penny!"},
		{"a slash, which a URL would eat", "stu/penny"},
		{"longer than the column deserves", strings.Repeat("a", 65)},
		// The important one: anything with an @ is judged as an address, so a
		// half-written address cannot slip through by being called an ID.
		{"half an address", "penny@student"},
		{"an @ and nothing else", "penny@"},
		{"a bare domain", "@jca.ac.th"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := auth.ValidateLoginID(tc.in); err == nil {
				t.Errorf("ValidateLoginID(%q) was accepted as %q", tc.in, got)
			}
		})
	}
}

// The distinction the reset endpoint turns on: it must not hand an ID to the
// mail sender, because there is no mailbox on the other end of one.
func TestLooksLikeEmailSeparatesIdsFromAddresses(t *testing.T) {
	for _, in := range []string{"head@jca.ac.th", "penny.ward@gmail.com"} {
		if !auth.LooksLikeEmail(in) {
			t.Errorf("LooksLikeEmail(%q) = false, want true", in)
		}
	}
	for _, in := range []string{"stu_penny_ward", "stu_john_smith_2", "", "penny@student"} {
		if auth.LooksLikeEmail(in) {
			t.Errorf("LooksLikeEmail(%q) = true — a reset link would be posted into the void", in)
		}
	}
}
