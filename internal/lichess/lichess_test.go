package lichess_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Kusk24/jtrax-backend/internal/lichess"
)

// The only place a caller-supplied string reaches the Lichess API is a URL
// path, so this is the boundary check that matters.
func TestValidUsername(t *testing.T) {
	for _, tc := range []struct {
		name string
		want bool
	}{
		{"thibault", true},
		{"Penny_Plays", true},
		{"a-b", true},
		{"Ab", true},
		{"", false},
		{"a", false}, // Lichess requires at least two
		{"has space", false},
		{"_leading", false}, // must start alphanumeric
		{"toolong" + strings.Repeat("x", 30), false},
		// The ones that matter: anything that would escape the path segment.
		{"../admin", false},
		{"a/b", false},
		{"a?b=c", false},
		{"a%2f", false},
		{"a\nb", false},
	} {
		if got := lichess.ValidUsername(tc.name); got != tc.want {
			t.Errorf("ValidUsername(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestBioContains(t *testing.T) {
	const code = "JTRAX-K7M2QX94"
	for _, tc := range []struct {
		name, bio string
		want      bool
	}{
		{"exactly the code", code, true},
		{"the code amid tidy text", "JCA Chess Academy — " + code + " — age 8", true},
		{"different case", strings.ToLower(code), true},
		{"empty bio", "", false},
		{"someone else's code", "JTRAX-AAAAAAAA", false},
		{"a prefix only", "JTRAX-K7M2", false},
	} {
		if got := lichess.BioContains(tc.bio, code); got != tc.want {
			t.Errorf("%s: BioContains(%q) = %v, want %v", tc.name, tc.bio, got, tc.want)
		}
	}
	// An empty code must never match, or a link with no outstanding code would
	// verify against any bio at all.
	if lichess.BioContains("anything at all", "") {
		t.Error("an empty code matched a bio")
	}
}

func TestUsersSendsOneRequestAndSkipsBadNames(t *testing.T) {
	var got string
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		buf := make([]byte, 4096)
		n, _ := r.Body.Read(buf)
		got = string(buf[:n])
		json.NewEncoder(w).Encode([]lichess.User{{ID: "thibault", Username: "thibault"}})
	}))
	defer srv.Close()

	c := lichess.New()
	c.BaseURL = srv.URL
	users, err := c.Users([]string{"thibault", "has space", "opperwezen", "../etc/passwd"})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Errorf("made %d requests for one batch, want 1", calls)
	}
	if got != "thibault,opperwezen" {
		t.Errorf("sent %q — malformed names should be dropped, not forwarded", got)
	}
	if len(users) != 1 {
		t.Errorf("got %d users", len(users))
	}
}

func TestUsersOfNothingMakesNoRequest(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Write([]byte("[]"))
	}))
	defer srv.Close()
	c := lichess.New()
	c.BaseURL = srv.URL
	if _, err := c.Users([]string{"!!", "  "}); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Errorf("made %d requests with nothing valid to ask for", calls)
	}
}

func TestRateLimitIsDistinguishable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	c := lichess.New()
	c.BaseURL = srv.URL
	// A 429 has to be its own error: the caller must back off rather than
	// treat it as a transient failure and retry into a block.
	if _, err := c.User("thibault"); err != lichess.ErrRateLimited {
		t.Errorf("got %v, want ErrRateLimited", err)
	}
}

func TestUnknownUserIsNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	c := lichess.New()
	c.BaseURL = srv.URL
	if _, err := c.User("nobody"); err != lichess.ErrNotFound {
		t.Errorf("got %v, want ErrNotFound", err)
	}
	// A malformed name never leaves the process.
	if _, err := c.User("../admin"); err != lichess.ErrNotFound {
		t.Errorf("malformed name: got %v, want ErrNotFound", err)
	}
}
