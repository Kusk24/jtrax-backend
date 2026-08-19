// The OAuth half of Lichess linking: the student grants the academy permission
// to play their games, and the academy stores the resulting token sealed.
//
// # Why this exists alongside the bio code
//
// The bio code in lichess.go proves an account is a student's, which is enough
// to display their rating. It cannot play a game. A rated game on Lichess is
// only possible with a token the account holder granted, so a student who wants
// their JTrax games to count has to come through here.
//
// Both paths remain. Granting play access is a bigger ask than pasting a code
// into a bio, and a student who only wants their rating on the wall should not
// have to hand over the ability to resign their games.
//
// # The callback carries no session
//
// The browser arrives back from lichess.org on a redirect, with no header of
// ours on it. The `state` row is what authenticates it: random, single-use,
// short-lived, and already bound to a student when the flow began. That is why
// state is looked up rather than trusted, marked used inside the same
// transaction that reads it, and why the endpoint is rate limited despite
// appearing to be "internal".
package api

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/Kusk24/jtrax-backend/internal/auth"
	"github.com/Kusk24/jtrax-backend/internal/httpx"
	"github.com/Kusk24/jtrax-backend/internal/lichess"
	"github.com/Kusk24/jtrax-backend/internal/secretbox"
)

// lichessKeyVar names the environment variable holding the sealing key for
// student tokens. Separate from the LINE key so that rotating one credential
// store does not invalidate the other.
const lichessKeyVar = "LICHESS_TOKEN_KEY"

// oauthStateTTL bounds how long a half-finished authorization stays usable.
//
// Long enough for a pupil to read a consent screen and ask a parent; short
// enough that an abandoned flow is not a live credential sitting in a table.
const oauthStateTTL = 15 * time.Minute

// tokenExpiryWarning is how close to expiry a link starts warning.
//
// Lichess tokens last a year and cannot be refreshed, so the failure mode
// without this is a rated game refused mid-tournament for a reason nobody can
// see. A month is enough notice to catch a student in a lesson.
const tokenExpiryWarning = 30 * 24 * time.Hour

type lichessOAuth struct {
	db  *sql.DB
	box *secretbox.Box
	// client is the authenticated client. The game stream opens its own
	// connection without a deadline, so the timeout here only bounds the
	// short calls: the token exchange and the account lookup.
	client *lichess.Client
	// clientID is any stable string. Lichess does not register applications;
	// it is shown to the student on the consent screen, so it should look like
	// the academy rather than like a random identifier.
	clientID string
	// redirectURI must be byte-identical between the authorize request and the
	// exchange, which is why it is configuration and not derived from the
	// inbound request — behind a proxy those differ in ways that are miserable
	// to debug.
	redirectURI string
	// returnAllowed are the portal origins a callback may bounce back to. An
	// unchecked return_to here would make the academy's own domain a usable hop
	// in a phishing chain.
	returnAllowed []string
}

func newState() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// configured reports whether the deployment can do OAuth at all.
//
// Without a sealing key the tokens could only be stored in the clear, which is
// not a trade worth offering: the feature turns itself off instead.
func (o *lichessOAuth) configured() bool {
	return o != nil && o.box != nil && o.redirectURI != ""
}

/* ---- token access ---- */

// errNoToken means the student has not granted play access.
var errNoToken = errors.New("lichess: no play token for student")

// errTokenExpired means the grant ran out. Distinct from errNoToken because the
// remedy the student is shown differs: one is "connect", the other "reconnect".
var errTokenExpired = errors.New("lichess: play token expired")

// playToken returns a student's decrypted Lichess token.
//
// The only place a token is ever unsealed. It is returned to callers in this
// package and never written to a response, a log line or an error.
func (o *lichessOAuth) playToken(studentID string) (username, token string, err error) {
	if !o.configured() {
		return "", "", errNoToken
	}
	var enc, expires sql.NullString
	err = o.db.QueryRow(`SELECT username, token_enc, token_expires_at
	                     FROM student_lichess WHERE student_id = ?`, studentID).
		Scan(&username, &enc, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", errNoToken
	}
	if err != nil {
		return "", "", err
	}
	if !enc.Valid || enc.String == "" {
		return "", "", errNoToken
	}
	if expires.Valid && expires.String != "" {
		if t, perr := time.Parse(sqliteTimeLayout, expires.String); perr == nil && time.Now().After(t) {
			return "", "", errTokenExpired
		}
	}
	tok, err := o.box.Open(enc.String)
	if err != nil {
		// A token that will not unseal means the key changed. Say so once, in
		// the log, without the ciphertext.
		return "", "", fmt.Errorf("lichess: could not unseal token for %s: %w", studentID, err)
	}
	return username, tok, nil
}

/* ---- start ---- */

// handleLichessOAuthStart begins the authorization flow.
//
// Returns the URL rather than redirecting: the caller is a fetch from the
// portal, and a 302 to lichess.org would be followed by the fetch instead of
// the browser, which is not what anybody wants.
func handleLichessOAuthStart(o *lichessOAuth) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := requireIdentity(o.db, w, r)
		if id == nil {
			return
		}
		if !o.configured() {
			httpx.Error(w, http.StatusServiceUnavailable, "Lichess play is not configured on this server", nil)
			return
		}

		var in struct {
			StudentID string `json:"studentId"`
			ReturnTo  string `json:"returnTo"`
		}
		// A body is optional: a student starting their own flow sends nothing.
		_ = httpx.Decode(r, &in)

		studentID, err := o.authorizeFor(id, in.StudentID)
		if err != nil {
			httpx.Error(w, http.StatusForbidden, "not allowed", nil)
			return
		}

		verifier, err := lichess.NewVerifier()
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not start", err)
			return
		}
		state, err := newState()
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not start", err)
			return
		}

		returnTo := o.safeReturn(in.ReturnTo)
		if _, err := o.db.Exec(`
			INSERT INTO lichess_oauth_state (state, code_verifier, student_id, started_by, return_to)
			VALUES (?, ?, ?, ?, ?)`,
			state, verifier, studentID, id.UserAccountID, returnTo); err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not start", err)
			return
		}
		// Old rows are cleared here rather than on a timer: this is the only
		// endpoint that creates them, so it is the natural place to tidy up and
		// costs one statement on a table that is never large.
		if _, err := o.db.Exec(`DELETE FROM lichess_oauth_state
		                        WHERE created_at < datetime('now', ?)`,
			fmt.Sprintf("-%d minutes", int(oauthStateTTL.Minutes()))); err != nil {
			log.Printf("lichess: pruning oauth state: %v", err)
		}

		url := o.client.AuthorizeURL(o.clientID, o.redirectURI, state,
			lichess.CodeChallenge(verifier), lichess.PlayScopes)
		httpx.JSON(w, http.StatusOK, map[string]any{"authorizeUrl": url})
	}
}

// authorizeFor decides whose account is being linked.
//
// A student links their own. Staff and a parent may run the flow *for* a child,
// which is the under-13 case: Lichess requires an account holder to be 13, so a
// younger pupil's account belongs to an adult who sits with them through the
// consent screen. Whoever ran it is recorded as managed_by.
func (o *lichessOAuth) authorizeFor(id *auth.Identity, want string) (string, error) {
	switch {
	case id.Role == "Student" && id.StudentID != "":
		if want != "" && want != id.StudentID {
			return "", errors.New("not allowed")
		}
		return id.StudentID, nil
	case isStaff(id.Role) || id.Role == "Teacher":
		if want == "" {
			return "", errors.New("studentId is required")
		}
		return want, nil
	case id.Role == "Parent":
		if want == "" {
			return "", errors.New("studentId is required")
		}
		// Enforced in the query, not by the caller: a parent may only start a
		// flow for a child who is actually theirs.
		var ok int
		if err := o.db.QueryRow(`SELECT COUNT(*) FROM student_parent
		                         WHERE student_id = ? AND parent_id = ?`,
			want, id.ParentID).Scan(&ok); err != nil || ok == 0 {
			return "", errors.New("not allowed")
		}
		return want, nil
	}
	return "", errors.New("not allowed")
}

// safeReturn keeps redirects inside the academy's own portals.
func (o *lichessOAuth) safeReturn(want string) string {
	if want == "" {
		return ""
	}
	u, err := url.Parse(want)
	if err != nil || !u.IsAbs() {
		return ""
	}
	origin := u.Scheme + "://" + u.Host
	for _, allowed := range o.returnAllowed {
		if allowed != "" && strings.EqualFold(origin, allowed) {
			return want
		}
	}
	return ""
}

/* ---- callback ---- */

// handleLichessOAuthCallback completes the flow and stores the token.
//
// Ends in a redirect back to the portal, because a person is looking at this
// URL in their address bar. The outcome travels as a query parameter the portal
// turns into a message; nothing sensitive goes in it.
func handleLichessOAuthCallback(o *lichessOAuth) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		state := q.Get("state")
		if state == "" {
			httpx.Error(w, http.StatusBadRequest, "missing state", nil)
			return
		}

		// Read and consume in one transaction. Two browsers replaying the same
		// callback must not both succeed, and this is the only thing stopping
		// them.
		tx, err := o.db.Begin()
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not complete", err)
			return
		}
		defer tx.Rollback()

		var verifier, studentID, startedBy string
		var returnTo sql.NullString
		var usedAt sql.NullString
		var createdAt string
		err = tx.QueryRow(`SELECT code_verifier, student_id, started_by, return_to, used_at, created_at
		                   FROM lichess_oauth_state WHERE state = ?`, state).
			Scan(&verifier, &studentID, &startedBy, &returnTo, &usedAt, &createdAt)
		if errors.Is(err, sql.ErrNoRows) {
			httpx.Error(w, http.StatusBadRequest, "that link is no longer valid", nil)
			return
		}
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not complete", err)
			return
		}
		if usedAt.Valid && usedAt.String != "" {
			log.Printf("lichess: oauth state replayed for student %s", studentID)
			httpx.Error(w, http.StatusBadRequest, "that link has already been used", nil)
			return
		}
		if t, perr := time.Parse(sqliteTimeLayout, createdAt); perr == nil && time.Since(t) > oauthStateTTL {
			httpx.Error(w, http.StatusBadRequest, "that link has expired, please try again", nil)
			return
		}
		if _, err := tx.Exec(`UPDATE lichess_oauth_state SET used_at = datetime('now') WHERE state = ?`, state); err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not complete", err)
			return
		}
		if err := tx.Commit(); err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not complete", err)
			return
		}

		// The student declining is a normal outcome, not an error.
		if e := q.Get("error"); e != "" {
			o.finish(w, r, returnTo.String, "declined")
			return
		}
		code := q.Get("code")
		if code == "" {
			o.finish(w, r, returnTo.String, "failed")
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()

		tok, err := o.client.Exchange(ctx, o.clientID, o.redirectURI, code, verifier)
		if err != nil {
			log.Printf("lichess: token exchange for %s: %v", studentID, err)
			o.finish(w, r, returnTo.String, "failed")
			return
		}
		// Whose token is it? Lichess is the only authority on that, and asking
		// is what makes the grant proof of ownership.
		acct, err := o.client.Account(ctx, tok.AccessToken)
		if err != nil {
			log.Printf("lichess: account lookup for %s: %v", studentID, err)
			o.finish(w, r, returnTo.String, "failed")
			return
		}

		if err := o.storeGrant(studentID, startedBy, acct, tok); err != nil {
			if errors.Is(err, errAccountTaken) {
				o.finish(w, r, returnTo.String, "taken")
				return
			}
			log.Printf("lichess: storing grant for %s: %v", studentID, err)
			o.finish(w, r, returnTo.String, "failed")
			return
		}
		// Ratings straight away, as the bio-code path already does. Otherwise a
		// pupil who has just connected lands back on a card reading "no rated
		// games yet" until the next scheduled sync, which is not true and reads
		// like the connection failed.
		o.storeInitialRatings(studentID, acct.Username)
		o.finish(w, r, returnTo.String, "connected")
	}
}

// errAccountTaken means another student already holds this Lichess account.
var errAccountTaken = errors.New("lichess: account already linked")

// storeInitialRatings fills in ratings right after a grant.
//
// Best effort: a pupil who has connected successfully must not be told the
// connection failed because a rating lookup was slow.
func (o *lichessOAuth) storeInitialRatings(studentID, username string) {
	u, err := o.client.User(username)
	if err != nil {
		log.Printf("lichess: initial ratings for %s: %v", studentID, err)
		return
	}
	if err := storeLichessRatings(o.db, studentID, *u); err != nil {
		log.Printf("lichess: storing initial ratings for %s: %v", studentID, err)
	}
}

func (o *lichessOAuth) storeGrant(studentID, startedBy string, acct *lichess.Account, tok *lichess.Token) error {
	lichessID := strings.ToLower(acct.ID)

	var owner string
	err := o.db.QueryRow(`SELECT student_id FROM student_lichess WHERE lichess_id = ?`, lichessID).Scan(&owner)
	if err == nil && owner != studentID {
		return errAccountTaken
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}

	sealed, err := o.box.Seal(tok.AccessToken)
	if err != nil {
		return err
	}
	expires := ""
	if !tok.ExpiresAt.IsZero() {
		expires = tok.ExpiresAt.UTC().Format(sqliteTimeLayout)
	}
	// managed_by is only set when somebody else ran the flow. A student linking
	// their own account is not "managed", and recording an adult there would
	// misrepresent who holds the password.
	var managedBy any
	if startedBy != "" && !o.startedByStudent(studentID, startedBy) {
		managedBy = startedBy
	}

	// verified = 1 unconditionally: the grant came from Lichess itself, which
	// is a stronger proof than the bio code this supersedes. The code is
	// cleared so a stale one cannot be shown next to a verified account.
	_, err = o.db.Exec(`
		INSERT INTO student_lichess (student_id, username, lichess_id, verified, verify_code,
		                             linked_at, token_enc, token_expires_at, token_scopes,
		                             authorized_at, managed_by)
		VALUES (?, ?, ?, 1, NULL, datetime('now'), ?, ?, ?, datetime('now'), ?)
		ON CONFLICT (student_id) DO UPDATE SET
		  username = excluded.username, lichess_id = excluded.lichess_id,
		  verified = 1, verify_code = NULL,
		  token_enc = excluded.token_enc, token_expires_at = excluded.token_expires_at,
		  token_scopes = excluded.token_scopes, authorized_at = excluded.authorized_at,
		  managed_by = excluded.managed_by`,
		studentID, acct.Username, lichessID, sealed, expires,
		strings.Join(lichess.PlayScopes, " "), managedBy)
	return err
}

// startedByStudent reports whether the account that ran the flow is the
// student's own login.
//
// The join lives on `student`, not on `user_account` — the latter has no
// student_id at all. Querying the wrong table here did not fail loudly; it
// returned an error that was read as "not the student", which quietly labelled
// every pupil who linked their own account as parent-managed.
func (o *lichessOAuth) startedByStudent(studentID, accountID string) bool {
	var n int
	if err := o.db.QueryRow(`SELECT COUNT(*) FROM student
	                         WHERE user_account_id = ? AND student_id = ?`,
		accountID, studentID).Scan(&n); err != nil {
		return false
	}
	return n > 0
}

// finish sends the browser home with an outcome it can render.
func (o *lichessOAuth) finish(w http.ResponseWriter, r *http.Request, returnTo, outcome string) {
	target := o.safeReturn(returnTo)
	if target == "" {
		// Nowhere safe to go: say it in plain text rather than redirect
		// somewhere unvalidated.
		httpx.JSON(w, http.StatusOK, map[string]any{"lichess": outcome})
		return
	}
	sep := "?"
	if strings.Contains(target, "?") {
		sep = "&"
	}
	http.Redirect(w, r, target+sep+"lichess="+url.QueryEscape(outcome), http.StatusSeeOther)
}

/* ---- status ---- */

// lichessPlayStatus is what a portal needs to decide which button to show.
type lichessPlayStatus struct {
	CanPlay      bool   `json:"canPlay"`
	Username     string `json:"username,omitempty"`
	ExpiresAt    string `json:"expiresAt,omitempty"`
	ExpiringSoon bool   `json:"expiringSoon"`
	Managed      bool   `json:"managed"`
}

// playStatusFor reports a student's play-access state without unsealing
// anything — a status endpoint has no business touching the token.
func (o *lichessOAuth) playStatusFor(studentID string) lichessPlayStatus {
	var st lichessPlayStatus
	if !o.configured() {
		return st
	}
	var enc, expires, managed sql.NullString
	var username string
	err := o.db.QueryRow(`SELECT username, token_enc, token_expires_at, managed_by
	                      FROM student_lichess WHERE student_id = ?`, studentID).
		Scan(&username, &enc, &expires, &managed)
	if err != nil || !enc.Valid || enc.String == "" {
		return st
	}
	st.Username = username
	st.Managed = managed.Valid && managed.String != ""
	if expires.Valid && expires.String != "" {
		st.ExpiresAt = sqliteISO(expires.String)
		if t, perr := time.Parse(sqliteTimeLayout, expires.String); perr == nil {
			if time.Now().After(t) {
				// Expired is not "can play". Saying otherwise would send a
				// student to a board that refuses their first move.
				return lichessPlayStatus{Username: username, ExpiresAt: st.ExpiresAt, Managed: st.Managed}
			}
			st.ExpiringSoon = time.Until(t) < tokenExpiryWarning
		}
	}
	st.CanPlay = true
	return st
}

func handleLichessPlayStatus(o *lichessOAuth) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := requireIdentity(o.db, w, r)
		if id == nil {
			return
		}
		studentID := r.URL.Query().Get("studentId")
		if studentID == "" {
			studentID = id.StudentID
		}
		// Reuse the scoped read as the authorization check.
		links, err := loadLinks(o.db, id, studentID)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not load", err)
			return
		}
		if len(links) == 0 {
			httpx.JSON(w, http.StatusOK, lichessPlayStatus{})
			return
		}
		httpx.JSON(w, http.StatusOK, o.playStatusFor(studentID))
	}
}

/* ---- disconnect ---- */

// revokeToken tells Lichess to drop the grant.
//
// Best effort by design: if Lichess is unreachable the local row still has to
// go, because a student who pressed disconnect must not be left connected. The
// failure is logged rather than surfaced.
func (o *lichessOAuth) revokeToken(studentID string) {
	_, tok, err := o.playToken(studentID)
	if err != nil || tok == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := o.client.Revoke(ctx, tok); err != nil {
		log.Printf("lichess: revoking token for %s: %v", studentID, err)
	}
}

/* ---- config ---- */

// lichessOAuthFromEnv builds the OAuth side, or a disabled one.
func lichessOAuthFromEnv(d *sql.DB) *lichessOAuth {
	o := &lichessOAuth{db: d, client: lichess.New()}
	if base := strings.TrimSpace(os.Getenv("LICHESS_API_BASE")); base != "" {
		o.client.BaseURL = base
	}

	box, err := secretbox.FromEnv(lichessKeyVar)
	if err != nil && !errors.Is(err, secretbox.ErrNoKey) {
		log.Printf("lichess: %s is set but unusable: %v", lichessKeyVar, err)
	}
	o.box = box

	apiURL := strings.TrimSuffix(strings.TrimSpace(os.Getenv("PUBLIC_API_URL")), "/")
	if apiURL != "" {
		o.redirectURI = apiURL + "/api/v1/lichess/oauth/callback"
	}

	o.clientID = strings.TrimSpace(os.Getenv("LICHESS_CLIENT_ID"))
	if o.clientID == "" {
		// Shown to the student on Lichess's consent screen, so it should read
		// like the academy rather than like a serial number.
		o.clientID = "jtrax.app"
	}

	for _, v := range []string{os.Getenv("APP_URL"), os.Getenv("ADMIN_URL")} {
		if v = strings.TrimSuffix(strings.TrimSpace(v), "/"); v != "" {
			o.returnAllowed = append(o.returnAllowed, v)
		}
	}
	if o.box == nil || o.redirectURI == "" {
		log.Printf("lichess: play access disabled (need %s and PUBLIC_API_URL)", lichessKeyVar)
	}
	return o
}
