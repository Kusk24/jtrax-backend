// Package lichess reads public player data from lichess.org.
//
// No credential of any kind. Lichess serves player profiles and ratings to
// anyone, with no key, no registration and no approval — which is why this
// package has no configuration and why the feature costs nothing to run.
//
// Only reads. Nothing here plays a game, joins a team or changes anything on
// Lichess; those all need an OAuth token belonging to the player, and the
// academy has no business holding one.
//
// # Why one bulk call rather than one per student
//
// Lichess asks integrators to be gentle and answers 429 when they are not.
// `Users` fetches up to 300 players in a single request, so a whole academy
// syncs in one call rather than one per pupil — the difference between a
// polite integration and a rude one.
package lichess

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// DefaultBaseURL is the public API host. Overridden in tests.
const DefaultBaseURL = "https://lichess.org"

// MaxBulkUsers is the documented ceiling for one /api/users request.
const MaxBulkUsers = 300

// usernamePattern is Lichess's own shape for a username.
//
// Checked before a name is ever put in a URL path, which is the only place a
// caller-supplied string reaches this API.
var usernamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{1,29}$`)

// ValidUsername reports whether s could be a Lichess username at all. A name
// that fails here is rejected at the boundary rather than sent upstream.
func ValidUsername(s string) bool { return usernamePattern.MatchString(s) }

// Perf is one rating: blitz, rapid, classical, puzzle and so on.
type Perf struct {
	Rating int  `json:"rating"`
	Games  int  `json:"games"`
	RD     int  `json:"rd"`
	Prog   int  `json:"prog"`
	Prov   bool `json:"prov"`
}

// User is the subset of a Lichess profile this product uses.
type User struct {
	ID       string          `json:"id"` // canonical, lowercase
	Username string          `json:"username"`
	Perfs    map[string]Perf `json:"perfs"`
	Profile  struct {
		Bio string `json:"bio"`
	} `json:"profile"`
	Disabled bool `json:"disabled"`
	TosViol  bool `json:"tosViolation"`
}

// TrackedPerfs are the ratings the academy shows. Lichess reports a dozen;
// these are the ones a coach would actually discuss, plus puzzle, which pairs
// with the academy's own puzzle record.
var TrackedPerfs = []string{"bullet", "blitz", "rapid", "classical", "puzzle"}

// Client reads from the public API.
type Client struct {
	BaseURL string
	HTTP    *http.Client
}

func New() *Client {
	return &Client{BaseURL: DefaultBaseURL, HTTP: &http.Client{Timeout: 15 * time.Second}}
}

// ErrNotFound means Lichess has no such account.
var ErrNotFound = errors.New("lichess: no such user")

// ErrRateLimited means Lichess asked us to slow down. Callers must back off
// rather than retry: hammering through a 429 is how an integration gets
// blocked outright.
var ErrRateLimited = errors.New("lichess: rate limited")

func (c *Client) base() string {
	if c.BaseURL == "" {
		return DefaultBaseURL
	}
	return c.BaseURL
}

func (c *Client) http() *http.Client {
	if c.HTTP == nil {
		return &http.Client{Timeout: 15 * time.Second}
	}
	return c.HTTP
}

// User fetches one player.
func (c *Client) User(username string) (*User, error) {
	if !ValidUsername(username) {
		return nil, ErrNotFound
	}
	res, err := c.http().Get(c.base() + "/api/user/" + username)
	if err != nil {
		return nil, fmt.Errorf("lichess: unreachable: %w", err)
	}
	defer res.Body.Close()
	if err := status(res); err != nil {
		return nil, err
	}
	var u User
	if err := json.NewDecoder(res.Body).Decode(&u); err != nil {
		return nil, fmt.Errorf("lichess: bad response: %w", err)
	}
	return &u, nil
}

// Users fetches many players in one request.
//
// Names that do not exist are simply absent from the reply rather than an
// error, so the caller matches on the returned ids — a student whose account
// was closed or renamed drops out of the sync instead of failing it for
// everyone else.
func (c *Client) Users(usernames []string) ([]User, error) {
	clean := make([]string, 0, len(usernames))
	for _, n := range usernames {
		if ValidUsername(n) {
			clean = append(clean, n)
		}
	}
	if len(clean) == 0 {
		return nil, nil
	}
	if len(clean) > MaxBulkUsers {
		clean = clean[:MaxBulkUsers]
	}
	res, err := c.http().Post(c.base()+"/api/users", "text/plain", strings.NewReader(strings.Join(clean, ",")))
	if err != nil {
		return nil, fmt.Errorf("lichess: unreachable: %w", err)
	}
	defer res.Body.Close()
	if err := status(res); err != nil {
		return nil, err
	}
	var users []User
	if err := json.NewDecoder(res.Body).Decode(&users); err != nil {
		return nil, fmt.Errorf("lichess: bad response: %w", err)
	}
	return users, nil
}

func status(res *http.Response) error {
	switch {
	case res.StatusCode == http.StatusNotFound:
		return ErrNotFound
	case res.StatusCode == http.StatusTooManyRequests:
		return ErrRateLimited
	case res.StatusCode < 200 || res.StatusCode >= 300:
		body, _ := io.ReadAll(io.LimitReader(res.Body, 512))
		return fmt.Errorf("lichess: status %d: %s", res.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

// BioContains reports whether a profile bio carries the given code.
//
// This is the whole of account verification, and it works because a bio is
// public but only its owner can edit it. Matching is case-insensitive and
// ignores surrounding text, so a student who writes "JTrax: CODE — my school"
// is not punished for being tidy.
func BioContains(bio, code string) bool {
	return code != "" && strings.Contains(strings.ToUpper(bio), strings.ToUpper(code))
}
