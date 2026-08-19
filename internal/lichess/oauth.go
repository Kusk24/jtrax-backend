// OAuth2 with PKCE against lichess.org: the authorize URL, and the exchange of
// an authorization code for an access token.
//
// # There is no client secret
//
// Lichess does not register applications. `client_id` is an arbitrary string
// the integrator picks, and the flow's security rests entirely on PKCE — the
// code is useless without the verifier that produced the challenge. So there is
// no secret to deploy, rotate or leak here, and the only credential this
// package ever handles is the student's own token coming back.
//
// # There is no refresh token
//
// Lichess returns expires_in of about a year and nothing to renew with. The
// honest model is therefore an expiry date and a re-link, not a refresh loop.
package lichess

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ScopeBoardPlay lets the holder play the account's games through the Board
// API: make moves, resign, offer and accept draws.
const ScopeBoardPlay = "board:play"

// ScopeChallengeWrite lets the holder create and accept challenges as the
// account. Pairing two students requires it on both sides.
const ScopeChallengeWrite = "challenge:write"

// PlayScopes are what the academy asks for. Deliberately the smallest set that
// can start and play a game — no email, no preferences, no team or study
// access, nothing that reads a child's private data.
var PlayScopes = []string{ScopeBoardPlay, ScopeChallengeWrite}

// verifierBytes is the entropy behind a PKCE verifier. RFC 7636 allows 43-128
// characters after encoding; 64 raw bytes lands at 86 and is comfortably past
// anything guessable.
const verifierBytes = 64

// NewVerifier returns a fresh PKCE code verifier.
func NewVerifier() (string, error) {
	raw := make([]byte, verifierBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("lichess: no entropy for pkce verifier: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// CodeChallenge derives the S256 code challenge sent to the authorize endpoint.
//
// The verifier itself must never travel with it — that is the entire point of
// PKCE, and sending both would reduce the flow to the plain method it replaced.
//
// Named for the OAuth term rather than just "Challenge" because this package
// also has chess challenges in it, and confusing the two would be a bad day.
func CodeChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// AuthorizeURL builds the address the student's browser is sent to.
func (c *Client) AuthorizeURL(clientID, redirectURI, state, challenge string, scopes []string) string {
	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", clientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("code_challenge_method", "S256")
	q.Set("code_challenge", challenge)
	q.Set("scope", strings.Join(scopes, " "))
	q.Set("state", state)
	return c.base() + "/oauth?" + q.Encode()
}

// Token is a granted access token and what came with it.
type Token struct {
	AccessToken string    `json:"access_token"`
	TokenType   string    `json:"token_type"`
	ExpiresIn   int       `json:"expires_in"`
	ExpiresAt   time.Time `json:"-"`
}

// Exchange trades an authorization code for an access token.
//
// redirectURI and clientID must match the ones used to obtain the code exactly;
// Lichess checks both, and a mismatch fails the exchange rather than silently
// issuing a weaker token.
func (c *Client) Exchange(ctx context.Context, clientID, redirectURI, code, verifier string) (*Token, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("code_verifier", verifier)
	form.Set("redirect_uri", redirectURI)
	form.Set("client_id", clientID)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base()+"/api/token",
		strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	res, err := c.http().Do(req)
	if err != nil {
		return nil, fmt.Errorf("lichess: token exchange unreachable: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		// The body carries an OAuth error document. It is read for the log and
		// deliberately not handed to the caller verbatim: it can echo the code
		// and the redirect back, and neither belongs in a client's error.
		body, _ := io.ReadAll(io.LimitReader(res.Body, 512))
		return nil, fmt.Errorf("lichess: token exchange refused (%d): %s",
			res.StatusCode, strings.TrimSpace(string(body)))
	}
	var t Token
	if err := json.NewDecoder(res.Body).Decode(&t); err != nil {
		return nil, fmt.Errorf("lichess: bad token response: %w", err)
	}
	if t.AccessToken == "" {
		return nil, fmt.Errorf("lichess: token response had no access token")
	}
	if t.ExpiresIn > 0 {
		t.ExpiresAt = time.Now().Add(time.Duration(t.ExpiresIn) * time.Second)
	}
	return &t, nil
}

// Revoke asks Lichess to invalidate a token.
//
// Called when a student unlinks. Deleting our copy would stop *us* using it but
// would leave a live grant on the student's account forever, which is not what
// "disconnect" means to the person clicking it.
func (c *Client) Revoke(ctx context.Context, token string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.base()+"/api/token", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := c.http().Do(req)
	if err != nil {
		return fmt.Errorf("lichess: revoke unreachable: %w", err)
	}
	defer res.Body.Close()
	return status(res)
}

// Account is the identity behind a token — used immediately after an exchange
// to learn *whose* token was just granted.
//
// This is the ownership proof. A student cannot complete Lichess's own consent
// screen for an account they cannot sign in to, so whatever comes back here is
// theirs by construction; nothing else needs checking.
type Account struct {
	ID       string `json:"id"`
	Username string `json:"username"`
}

// Account reads the profile the token belongs to.
func (c *Client) Account(ctx context.Context, token string) (*Account, error) {
	res, err := c.do(ctx, http.MethodGet, "/api/account", token, nil)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if err := status(res); err != nil {
		return nil, err
	}
	var a Account
	if err := json.NewDecoder(res.Body).Decode(&a); err != nil {
		return nil, fmt.Errorf("lichess: bad account response: %w", err)
	}
	return &a, nil
}

/* ---- plumbing ---- */

// do issues an authenticated request. Lives here rather than with the play
// calls because the first authenticated call any token makes is the account
// lookup above, which is what turns a grant into proof of ownership.
func (c *Client) do(ctx context.Context, method, path, token string, form url.Values) (*http.Response, error) {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base()+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	res, err := c.http().Do(req)
	if err != nil {
		return nil, fmt.Errorf("lichess: unreachable: %w", err)
	}
	return res, nil
}
