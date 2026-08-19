// Lichess account links and the ratings synced from them.
//
// Students play at home on Lichess. The academy sees everything played here —
// game_room records every move — and nothing played there, so these endpoints
// close the half of a pupil's practice the school is otherwise blind to.
//
// Two things shape the design:
//
//   - **Lichess pushes nothing.** There is no webhook and no subscription, so
//     "synced" means the server reads on a schedule. It reads the whole academy
//     in one request rather than one per pupil.
//   - **A typed username is a claim, not a fact.** Anyone can enter a
//     grandmaster's account. Verification is a one-time code the student puts
//     in their Lichess bio, which is public to read and private to write.
package api

import (
	"crypto/rand"
	"database/sql"
	"errors"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/Kusk24/jtrax-backend/internal/auth"
	"github.com/Kusk24/jtrax-backend/internal/httpx"
	"github.com/Kusk24/jtrax-backend/internal/lichess"
)

// syncInterval is how stale a reading may be before a read refreshes it.
//
// Ratings move over days, not seconds, and Lichess asks integrators to be
// gentle — so the sync is lazy: a request finds the data stale and refreshes
// it, rather than a timer firing against a service that spends the night
// asleep on the free tier.
const syncInterval = 6 * time.Hour

// verifyAlphabet omits I, O, 0 and 1 — a code is read off one screen and typed
// into another, and those four are where that goes wrong.
const verifyAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

const verifyCodeLength = 8

type lichessDeps struct {
	db     *sql.DB
	client *lichess.Client
	// oauth is the play-access half. Held here so unlinking can revoke the
	// grant on Lichess rather than only forgetting it locally.
	oauth *lichessOAuth
	// One sync at a time. Without this, a class of pupils opening the app at
	// once would each trigger their own full fetch.
	mu       sync.Mutex
	lastSync time.Time
}

func newVerifyCode() string {
	raw := make([]byte, verifyCodeLength)
	if _, err := rand.Read(raw); err != nil {
		return "JTRAX-" + strings.ToUpper(newID("v")[2:])
	}
	out := make([]byte, verifyCodeLength)
	for i, b := range raw {
		out[i] = verifyAlphabet[int(b)%len(verifyAlphabet)]
	}
	return "JTRAX-" + string(out)
}

/* ---- views ---- */

type lichessRating struct {
	Perf        string `json:"perf"`
	Rating      int    `json:"rating"`
	Games       int    `json:"games"`
	Provisional bool   `json:"provisional"`
}

type lichessLink struct {
	StudentID string          `json:"studentId"`
	Name      string          `json:"studentName,omitempty"`
	Username  string          `json:"username"`
	LichessID string          `json:"lichessId"`
	Verified  bool            `json:"verified"`
	ByStaff   bool            `json:"addedByStaff"`
	LinkedAt  string          `json:"linkedAt"`
	SyncedAt  string          `json:"syncedAt,omitempty"`
	Ratings   []lichessRating `json:"ratings"`
	/** Only ever returned to the student who owns the link, and only while
	  unverified — it is an instruction, not a secret worth hiding. */
	VerifyCode string `json:"verifyCode,omitempty"`
	ProfileURL string `json:"profileUrl"`
}

func lichessProfileURL(username string) string { return "https://lichess.org/@/" + username }

// loadLinks reads links and their ratings, restricted to what the caller may
// see. The scope is decided here, in the query, not by the caller.
func loadLinks(d *sql.DB, id *auth.Identity, only string) ([]lichessLink, error) {
	where := []string{}
	args := []any{}
	switch {
	case isStaff(id.Role) || id.Role == "Teacher":
		// Teachers already list every student; a rating is less than a name.
	case id.Role == "Parent":
		where = append(where, `l.student_id IN (SELECT student_id FROM student_parent WHERE parent_id = ?)`)
		args = append(args, id.ParentID)
	case id.Role == "Student":
		where = append(where, `l.student_id = ?`)
		args = append(args, id.StudentID)
	default:
		return []lichessLink{}, nil
	}
	if only != "" {
		where = append(where, `l.student_id = ?`)
		args = append(args, only)
	}
	q := `SELECT l.student_id, COALESCE(s.name,''), l.username, l.lichess_id, l.verified,
	             l.linked_by IS NOT NULL AND l.verified = 0, l.linked_at, COALESCE(l.synced_at,'')
	      FROM student_lichess l LEFT JOIN student s ON s.student_id = l.student_id`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY s.name, l.student_id"

	rows, err := d.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []lichessLink{}
	byStudent := map[string]int{}
	for rows.Next() {
		var l lichessLink
		var verified, byStaff int
		if err := rows.Scan(&l.StudentID, &l.Name, &l.Username, &l.LichessID,
			&verified, &byStaff, &l.LinkedAt, &l.SyncedAt); err != nil {
			return nil, err
		}
		l.Verified = verified == 1
		l.ByStaff = byStaff == 1
		l.Ratings = []lichessRating{}
		l.ProfileURL = lichessProfileURL(l.Username)
		byStudent[l.StudentID] = len(out)
		out = append(out, l)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return out, nil
	}

	rrows, err := d.Query(`SELECT student_id, perf, rating, games, provisional FROM lichess_rating`)
	if err != nil {
		return nil, err
	}
	defer rrows.Close()
	for rrows.Next() {
		var sid string
		var r lichessRating
		var prov int
		if err := rrows.Scan(&sid, &r.Perf, &r.Rating, &r.Games, &prov); err != nil {
			return nil, err
		}
		r.Provisional = prov == 1
		if i, ok := byStudent[sid]; ok {
			out[i].Ratings = append(out[i].Ratings, r)
		}
	}
	// Stable, meaningful order rather than whatever the table returns.
	rank := map[string]int{}
	for i, p := range lichess.TrackedPerfs {
		rank[p] = i
	}
	for i := range out {
		rs := out[i].Ratings
		for a := 1; a < len(rs); a++ {
			for b := a; b > 0 && rank[rs[b].Perf] < rank[rs[b-1].Perf]; b-- {
				rs[b], rs[b-1] = rs[b-1], rs[b]
			}
		}
	}
	return out, rrows.Err()
}

/* ---- sync ---- */

// syncAll refreshes every linked account from Lichess.
//
// One request for the whole academy. Accounts Lichess does not return — closed,
// renamed, misspelled — are left with their last known ratings and an unchanged
// synced_at, so one bad username cannot blank the class.
func (l *lichessDeps) syncAll(force bool) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !force && time.Since(l.lastSync) < syncInterval {
		return 0, nil
	}

	rows, err := l.db.Query(`SELECT student_id, lichess_id FROM student_lichess`)
	if err != nil {
		return 0, err
	}
	byID := map[string]string{}
	names := []string{}
	for rows.Next() {
		var sid, lid string
		if err := rows.Scan(&sid, &lid); err != nil {
			rows.Close()
			return 0, err
		}
		byID[lid] = sid
		names = append(names, lid)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	// Marked done even with nothing to fetch, so an academy with no links does
	// not retry on every request.
	l.lastSync = time.Now()
	if len(names) == 0 {
		return 0, nil
	}

	updated := 0
	for start := 0; start < len(names); start += lichess.MaxBulkUsers {
		end := min(start+lichess.MaxBulkUsers, len(names))
		users, err := l.client.Users(names[start:end])
		if err != nil {
			// A rate limit is a reason to stop, not to keep pushing.
			if errors.Is(err, lichess.ErrRateLimited) {
				log.Printf("lichess: rate limited, backing off")
				return updated, err
			}
			log.Printf("lichess: bulk fetch failed: %v", err)
			return updated, err
		}
		for _, u := range users {
			sid, ok := byID[strings.ToLower(u.ID)]
			if !ok {
				continue
			}
			if err := l.storeRatings(sid, u); err != nil {
				log.Printf("lichess: store %s: %v", sid, err)
				continue
			}
			updated++
		}
	}
	return updated, nil
}

func (l *lichessDeps) storeRatings(studentID string, u lichess.User) error {
	return storeLichessRatings(l.db, studentID, u)
}

// storeLichessRatings writes one player's ratings.
//
// A free function rather than only a method because the OAuth callback needs it
// too, and that path has no lichessDeps to hand.
func storeLichessRatings(db *sql.DB, studentID string, u lichess.User) error {
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	day := time.Now().UTC().Format("2006-01-02")
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, perf := range lichess.TrackedPerfs {
		p, ok := u.Perfs[perf]
		// A perf the player has never touched is absent, not zero — recording
		// it as 0 would put a beginner at the bottom of a leaderboard for a
		// game type they have simply never played.
		if !ok || p.Games == 0 {
			continue
		}
		prov := 0
		if p.Prov {
			prov = 1
		}
		if _, err := tx.Exec(`
			INSERT INTO lichess_rating (student_id, perf, rating, games, provisional, synced_at)
			VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT (student_id, perf) DO UPDATE SET
			  rating = excluded.rating, games = excluded.games,
			  provisional = excluded.provisional, synced_at = excluded.synced_at`,
			studentID, perf, p.Rating, p.Games, prov, now); err != nil {
			return err
		}
		// First reading of the day wins, so syncing repeatedly does not rewrite
		// history behind a chart someone is looking at.
		if _, err := tx.Exec(`
			INSERT OR IGNORE INTO lichess_rating_day (student_id, perf, on_date, rating, games)
			VALUES (?, ?, ?, ?, ?)`, studentID, perf, day, p.Rating, p.Games); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`UPDATE student_lichess SET synced_at = ? WHERE student_id = ?`, now, studentID); err != nil {
		return err
	}
	return tx.Commit()
}

/* ---- endpoints ---- */

func handleLichessList(l *lichessDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := requireIdentity(l.db, w, r)
		if id == nil {
			return
		}
		if _, err := l.syncAll(false); err != nil {
			// A failed refresh is not a failed read: the stored ratings are
			// still the truth as of the last successful sync.
			log.Printf("lichess: background sync: %v", err)
		}
		links, err := loadLinks(l.db, id, r.URL.Query().Get("studentId"))
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not load links", err)
			return
		}
		httpx.JSON(w, http.StatusOK, links)
	}
}

// handleLichessMine returns the caller's own link, with the verification code
// while one is outstanding.
func handleLichessMine(l *lichessDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := requireIdentity(l.db, w, r)
		if id == nil {
			return
		}
		if id.Role != "Student" || id.StudentID == "" {
			httpx.Error(w, http.StatusForbidden, "not allowed", nil)
			return
		}
		if _, err := l.syncAll(false); err != nil {
			log.Printf("lichess: background sync: %v", err)
		}
		links, err := loadLinks(l.db, id, id.StudentID)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not load link", err)
			return
		}
		if len(links) == 0 {
			httpx.JSON(w, http.StatusOK, map[string]any{"linked": false})
			return
		}
		link := links[0]
		if !link.Verified {
			var code sql.NullString
			_ = l.db.QueryRow(`SELECT verify_code FROM student_lichess WHERE student_id = ?`, id.StudentID).Scan(&code)
			link.VerifyCode = code.String
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"linked": true, "link": link})
	}
}

func handleLichessLink(l *lichessDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := requireIdentity(l.db, w, r)
		if id == nil {
			return
		}
		var in struct {
			Username  string `json:"username"`
			StudentID string `json:"studentId"`
		}
		if err := httpx.Decode(r, &in); err != nil {
			httpx.Error(w, http.StatusBadRequest, "username is required", err)
			return
		}
		username := strings.TrimSpace(in.Username)
		if !lichess.ValidUsername(username) {
			httpx.Error(w, http.StatusBadRequest, "that is not a valid Lichess username", nil)
			return
		}

		// A student links their own account and nobody else's; staff may link
		// on a pupil's behalf. Everyone else is refused.
		studentID := in.StudentID
		staffLinked := false
		switch {
		case id.Role == "Student" && id.StudentID != "":
			if studentID != "" && studentID != id.StudentID {
				httpx.Error(w, http.StatusForbidden, "not allowed", nil)
				return
			}
			studentID = id.StudentID
		case isStaff(id.Role):
			if studentID == "" {
				httpx.Error(w, http.StatusBadRequest, "studentId is required", nil)
				return
			}
			staffLinked = true
		default:
			httpx.Error(w, http.StatusForbidden, "not allowed", nil)
			return
		}

		user, err := l.client.User(username)
		if err != nil {
			if errors.Is(err, lichess.ErrNotFound) {
				httpx.Error(w, http.StatusNotFound, "no Lichess account with that name", nil)
				return
			}
			if errors.Is(err, lichess.ErrRateLimited) {
				httpx.Error(w, http.StatusTooManyRequests, "Lichess is busy, try again shortly", err)
				return
			}
			httpx.Error(w, http.StatusBadGateway, "could not reach Lichess", err)
			return
		}
		if user.Disabled {
			httpx.Error(w, http.StatusBadRequest, "that Lichess account is closed", nil)
			return
		}

		var owner string
		err = l.db.QueryRow(`SELECT student_id FROM student_lichess WHERE lichess_id = ?`,
			strings.ToLower(user.ID)).Scan(&owner)
		if err == nil && owner != studentID {
			httpx.Error(w, http.StatusConflict, "that Lichess account is already linked to another student", nil)
			return
		}

		code := newVerifyCode()
		linkedBy := any(nil)
		if staffLinked {
			linkedBy = id.UserAccountID
		}
		if _, err := l.db.Exec(`
			INSERT INTO student_lichess (student_id, username, lichess_id, verified, verify_code, linked_at, linked_by)
			VALUES (?, ?, ?, 0, ?, datetime('now'), ?)
			ON CONFLICT (student_id) DO UPDATE SET
			  username = excluded.username, lichess_id = excluded.lichess_id,
			  verified = 0, verify_code = excluded.verify_code,
			  linked_at = excluded.linked_at, linked_by = excluded.linked_by, synced_at = NULL`,
			studentID, user.Username, strings.ToLower(user.ID), code, linkedBy); err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not save the link", err)
			return
		}
		// Ratings are stored straight away, so the screen has something on it
		// before the student gets round to editing their bio.
		if err := l.storeRatings(studentID, *user); err != nil {
			log.Printf("lichess: initial ratings for %s: %v", studentID, err)
		}

		out := map[string]any{
			"studentId": studentID, "username": user.Username, "lichessId": strings.ToLower(user.ID),
			"verified": false, "profileUrl": lichessProfileURL(user.Username),
		}
		// Staff cannot edit a pupil's Lichess bio, so handing them the code
		// would only invite them to pass it around. The pupil sees it on their
		// own screen.
		if !staffLinked {
			out["verifyCode"] = code
		}
		httpx.JSON(w, http.StatusCreated, out)
	}
}

// handleLichessVerify checks the code in the student's Lichess bio.
//
// The bio is public to read and private to write, which is exactly the property
// account verification needs, and it costs one API call instead of an OAuth
// round trip.
func handleLichessVerify(l *lichessDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := requireIdentity(l.db, w, r)
		if id == nil {
			return
		}
		if id.Role != "Student" || id.StudentID == "" {
			httpx.Error(w, http.StatusForbidden, "not allowed", nil)
			return
		}
		var username, code string
		var verified int
		err := l.db.QueryRow(`SELECT username, COALESCE(verify_code,''), verified
		                      FROM student_lichess WHERE student_id = ?`, id.StudentID).
			Scan(&username, &code, &verified)
		if errors.Is(err, sql.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, "link a Lichess account first", nil)
			return
		}
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not read the link", err)
			return
		}
		if verified == 1 {
			httpx.JSON(w, http.StatusOK, map[string]any{"verified": true})
			return
		}
		user, err := l.client.User(username)
		if err != nil {
			httpx.Error(w, http.StatusBadGateway, "could not reach Lichess", err)
			return
		}
		if !lichess.BioContains(user.Profile.Bio, code) {
			// No hint about what was found: the student knows their own bio,
			// and describing it back is a way to probe someone else's.
			httpx.JSON(w, http.StatusOK, map[string]any{"verified": false, "reason": "codeNotFound"})
			return
		}
		if _, err := l.db.Exec(`UPDATE student_lichess SET verified = 1, verify_code = NULL
		                        WHERE student_id = ?`, id.StudentID); err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not save", err)
			return
		}
		if err := l.storeRatings(id.StudentID, *user); err != nil {
			log.Printf("lichess: ratings after verify for %s: %v", id.StudentID, err)
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"verified": true})
	}
}

func handleLichessUnlink(l *lichessDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := requireIdentity(l.db, w, r)
		if id == nil {
			return
		}
		studentID := r.URL.Query().Get("studentId")
		switch {
		case id.Role == "Student" && id.StudentID != "":
			if studentID != "" && studentID != id.StudentID {
				httpx.Error(w, http.StatusForbidden, "not allowed", nil)
				return
			}
			studentID = id.StudentID
		case isStaff(id.Role):
			if studentID == "" {
				httpx.Error(w, http.StatusBadRequest, "studentId is required", nil)
				return
			}
		default:
			httpx.Error(w, http.StatusForbidden, "not allowed", nil)
			return
		}
		// Revoked before the row goes, because revoking needs the token that is
		// about to be deleted. Deleting only our copy would leave a live grant
		// on the student's Lichess account forever, which is not what the
		// person pressing "disconnect" is asking for.
		l.oauth.revokeToken(studentID)

		tx, err := l.db.Begin()
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not unlink", err)
			return
		}
		defer tx.Rollback()
		for _, q := range []string{
			`DELETE FROM lichess_oauth_state WHERE student_id = ?`,
			`DELETE FROM lichess_rating_day WHERE student_id = ?`,
			`DELETE FROM lichess_rating WHERE student_id = ?`,
			`DELETE FROM student_lichess WHERE student_id = ?`,
		} {
			if _, err := tx.Exec(q, studentID); err != nil {
				httpx.Error(w, http.StatusInternalServerError, "could not unlink", err)
				return
			}
		}
		if err := tx.Commit(); err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not unlink", err)
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"linked": false})
	}
}

// handleLichessSync forces a refresh. Staff only: it is one outbound request
// for the whole academy and does not need to be reachable by every pupil.
func handleLichessSync(l *lichessDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if requireStaff(l.db, w, r) == nil {
			return
		}
		n, err := l.syncAll(true)
		if err != nil {
			if errors.Is(err, lichess.ErrRateLimited) {
				httpx.Error(w, http.StatusTooManyRequests, "Lichess asked us to slow down, try again shortly", err)
				return
			}
			httpx.Error(w, http.StatusBadGateway, "could not reach Lichess", err)
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"synced": n})
	}
}

// handleLichessHistory returns a student's rating over time for one perf.
func handleLichessHistory(l *lichessDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := requireIdentity(l.db, w, r)
		if id == nil {
			return
		}
		studentID := r.PathValue("studentId")
		// Reuse the scoped read as the authorization check: if the caller
		// cannot see the link, they cannot see its history either.
		links, err := loadLinks(l.db, id, studentID)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not load", err)
			return
		}
		if len(links) == 0 {
			httpx.Error(w, http.StatusNotFound, "not found", nil)
			return
		}
		perf := r.URL.Query().Get("perf")
		if perf == "" {
			perf = "rapid"
		}
		rows, err := l.db.Query(`SELECT on_date, rating, games FROM lichess_rating_day
		                         WHERE student_id = ? AND perf = ? ORDER BY on_date`, studentID, perf)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not load history", err)
			return
		}
		defer rows.Close()
		type point struct {
			Date   string `json:"date"`
			Rating int    `json:"rating"`
			Games  int    `json:"games"`
		}
		out := []point{}
		for rows.Next() {
			var p point
			if err := rows.Scan(&p.Date, &p.Rating, &p.Games); err != nil {
				httpx.Error(w, http.StatusInternalServerError, "could not load history", err)
				return
			}
			out = append(out, p)
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"perf": perf, "points": out})
	}
}

/* ---- mount ---- */

func mountLichess(mux *http.ServeMux, d *sql.DB) {
	deps := &lichessDeps{db: d, client: lichess.New(), oauth: lichessOAuthFromEnv(d)}
	if base := strings.TrimSpace(os.Getenv("LICHESS_API_BASE")); base != "" {
		// A seam for tests and for a deployment behind an egress proxy.
		deps.client.BaseURL = base
	}
	const p = "/api/v1/lichess"

	// The OAuth pair. Start is signed in; the callback cannot be — the browser
	// arrives on a redirect from lichess.org — so its budget is tighter and the
	// single-use state row is what authenticates it.
	mux.HandleFunc("POST "+p+"/oauth/start", httpx.RateLimit(20, handleLichessOAuthStart(deps.oauth)))
	mux.HandleFunc("GET "+p+"/oauth/callback", httpx.RateLimit(30, handleLichessOAuthCallback(deps.oauth)))
	mux.HandleFunc("GET "+p+"/play-status", handleLichessPlayStatus(deps.oauth))

	mux.HandleFunc("GET "+p+"/links", handleLichessList(deps))
	mux.HandleFunc("GET "+p+"/me", handleLichessMine(deps))
	// Both of these make an outbound request on the caller's behalf, so they
	// carry a budget even though the caller is signed in.
	mux.HandleFunc("POST "+p+"/link", httpx.RateLimit(20, handleLichessLink(deps)))
	mux.HandleFunc("POST "+p+"/verify", httpx.RateLimit(20, handleLichessVerify(deps)))
	mux.HandleFunc("DELETE "+p+"/link", handleLichessUnlink(deps))
	mux.HandleFunc("POST "+p+"/sync", httpx.RateLimit(10, handleLichessSync(deps)))
	mux.HandleFunc("GET "+p+"/history/{studentId}", handleLichessHistory(deps))
}
