// Student-to-student challenges: finding an opponent, inviting them, and the
// room that appears when they say yes.
//
// # What this endpoint exposes, and why that needed a decision
//
// Searching by name means any signed-in pupil can list other children's names.
// That is a deliberate product choice, made explicitly — the alternative
// (classmates only) means two friends in different class times can never
// challenge each other. What the choice does *not* extend to is everything
// else: the search returns a name, an id, and whether the account can play
// rated. Never an email, never a date of birth, never a parent, never a rating.
//
// The id is the one a pupil finds on their own profile, so a friend in another
// class can be reached by sharing it — the exact-match path exists so that
// works without anybody having to be found by name at all.
package api

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"github.com/Kusk24/jtrax-backend/internal/game"
	"github.com/Kusk24/jtrax-backend/internal/httpx"
)

// searchLimit caps a page of results. A search is a way to find one person, not
// a way to page through the academy.
const searchLimit = 20

// minSearchLen stops a single letter returning most of the school.
const minSearchLen = 2

type playerResult struct {
	StudentID string `json:"studentId"`
	Name      string `json:"name"`
	// Whether this player has granted Lichess play access. A rated challenge
	// needs it from both sides, so the person choosing needs to see it before
	// they choose.
	CanPlayRated bool `json:"canPlayRated"`
}

// handleSearchPlayers finds someone to challenge.
//
// Two ways to match, deliberately different in shape: an exact student id
// returns exactly that pupil, and a name fragment returns everyone it matches.
func handleSearchPlayers(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := requireIdentity(d, w, r)
		if id == nil {
			return
		}
		if !canPlay(id.Role) {
			httpx.Error(w, http.StatusForbidden, "only students and teachers can search for an opponent", nil)
			return
		}
		q := strings.TrimSpace(r.URL.Query().Get("q"))
		if len([]rune(q)) < minSearchLen {
			// Not an error: an empty box is the normal state of a search screen.
			httpx.JSON(w, http.StatusOK, map[string]any{"players": []playerResult{}})
			return
		}

		rows, err := d.Query(`
			SELECT s.student_id, s.name,
			       EXISTS(SELECT 1 FROM student_lichess sl
			               WHERE sl.student_id = s.student_id AND sl.token_enc IS NOT NULL)
			FROM student s
			JOIN user_account u ON u.user_account_id = s.user_account_id
			-- Never the caller: challenging yourself is the one thing the room
			-- table refuses outright, so it must not be offered here.
			WHERE u.user_account_id <> ?
			  AND (s.student_id = ? OR lower(s.name) LIKE ?)
			ORDER BY
			  -- An exact id match is what the searcher meant; it goes first.
			  CASE WHEN s.student_id = ? THEN 0 ELSE 1 END,
			  s.name
			LIMIT ?`,
			id.UserAccountID, q, "%"+strings.ToLower(q)+"%", q, searchLimit)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not search", err)
			return
		}
		defer rows.Close()

		out := []playerResult{}
		for rows.Next() {
			var p playerResult
			if err := rows.Scan(&p.StudentID, &p.Name, &p.CanPlayRated); err != nil {
				httpx.Error(w, http.StatusInternalServerError, "could not search", err)
				return
			}
			out = append(out, p)
		}
		if err := rows.Err(); err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not search", err)
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"players": out})
	}
}

/* ---- challenges ---- */

type challengeView struct {
	ID string `json:"challengeId"`
	// "in" when somebody is waiting on this caller, "out" when the caller is
	// waiting on somebody. The screen reads completely differently for each.
	Direction      string `json:"direction"`
	OpponentName   string `json:"opponentName"`
	OpponentID     string `json:"opponentStudentId,omitempty"`
	Status         string `json:"status"`
	Rated          bool   `json:"rated"`
	ClockLimit     int    `json:"clockLimit"`
	ClockIncrement int    `json:"clockIncrement"`
	GameRoomID     string `json:"gameRoomId,omitempty"`
	CreatedAt      string `json:"createdAt"`
	// Whether a rated game could actually go ahead. Shown on a pending
	// challenge so neither side accepts expecting a rated game and gets an
	// unrated one.
	BothCanPlayRated bool `json:"bothCanPlayRated"`
}

// handleListChallenges returns the caller's live invitations, both directions.
func handleListChallenges(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := requireIdentity(d, w, r)
		if id == nil {
			return
		}
		if !canPlay(id.Role) {
			httpx.Error(w, http.StatusForbidden, "not allowed", nil)
			return
		}
		rows, err := d.Query(`
			SELECT c.game_challenge_id, c.from_account_id, c.to_account_id, c.status,
			       c.rated, c.clock_limit, c.clock_increment,
			       COALESCE(c.game_room_id,''), c.created_at,
			       COALESCE(fs.name, fu.display_name), COALESCE(fs.student_id,''),
			       COALESCE(ts.name, tu.display_name), COALESCE(ts.student_id,''),
			       EXISTS(SELECT 1 FROM student_lichess sl WHERE sl.student_id = fs.student_id AND sl.token_enc IS NOT NULL),
			       EXISTS(SELECT 1 FROM student_lichess sl WHERE sl.student_id = ts.student_id AND sl.token_enc IS NOT NULL)
			FROM game_challenge c
			JOIN user_account fu ON fu.user_account_id = c.from_account_id
			JOIN user_account tu ON tu.user_account_id = c.to_account_id
			LEFT JOIN student fs ON fs.user_account_id = c.from_account_id
			LEFT JOIN student ts ON ts.user_account_id = c.to_account_id
			-- Scoped in the WHERE clause, not filtered afterwards: a caller's
			-- list can never contain somebody else's invitation.
			WHERE (c.from_account_id = ? OR c.to_account_id = ?)
			  AND c.status IN ('Pending','Accepted')
			ORDER BY c.created_at DESC
			LIMIT 50`, id.UserAccountID, id.UserAccountID)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not load challenges", err)
			return
		}
		defer rows.Close()

		out := []challengeView{}
		for rows.Next() {
			var c challengeView
			var from, to, fromName, fromSid, toName, toSid string
			var fromRated, toRated bool
			if err := rows.Scan(&c.ID, &from, &to, &c.Status, &c.Rated,
				&c.ClockLimit, &c.ClockIncrement, &c.GameRoomID, &c.CreatedAt,
				&fromName, &fromSid, &toName, &toSid, &fromRated, &toRated); err != nil {
				httpx.Error(w, http.StatusInternalServerError, "could not load challenges", err)
				return
			}
			if from == id.UserAccountID {
				c.Direction, c.OpponentName, c.OpponentID = "out", toName, toSid
			} else {
				c.Direction, c.OpponentName, c.OpponentID = "in", fromName, fromSid
			}
			c.BothCanPlayRated = fromRated && toRated
			out = append(out, c)
		}
		if err := rows.Err(); err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not load challenges", err)
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"challenges": out})
	}
}

// handleCreateChallenge invites one player to a game.
func handleCreateChallenge(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := requireIdentity(d, w, r)
		if id == nil {
			return
		}
		if !canPlay(id.Role) {
			httpx.Error(w, http.StatusForbidden, "only students and teachers can send a challenge", nil)
			return
		}
		var in struct {
			StudentID      string `json:"studentId"`
			Rated          bool   `json:"rated"`
			ClockLimit     int    `json:"clockLimit"`
			ClockIncrement int    `json:"clockIncrement"`
		}
		if err := httpx.Decode(r, &in); err != nil {
			httpx.Error(w, http.StatusBadRequest, "invalid body", err)
			return
		}
		limit, increment, err := lichessClock(in.ClockLimit, in.ClockIncrement)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, err.Error(), nil)
			return
		}

		var toAccount string
		err = d.QueryRow(`SELECT u.user_account_id FROM student s
		                  JOIN user_account u ON u.user_account_id = s.user_account_id
		                  WHERE s.student_id = ?`, strings.TrimSpace(in.StudentID)).Scan(&toAccount)
		if errors.Is(err, sql.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, "no player with that id", nil)
			return
		}
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not send the challenge", err)
			return
		}
		if toAccount == id.UserAccountID {
			httpx.Error(w, http.StatusBadRequest, "you cannot challenge yourself", nil)
			return
		}

		rated := 0
		if in.Rated {
			rated = 1
		}
		challengeID := newID("chl")
		_, err = d.Exec(`INSERT INTO game_challenge
			(game_challenge_id, from_account_id, to_account_id, rated, clock_limit, clock_increment, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			challengeID, id.UserAccountID, toAccount, rated, limit, increment, sqliteNow())
		if err != nil {
			// The partial unique index is the last word: re-sending is answered
			// as a conflict rather than filling somebody's inbox with copies.
			if isUniqueViolation(err) {
				httpx.Error(w, http.StatusConflict, "you have already challenged this player", nil)
				return
			}
			httpx.Error(w, http.StatusInternalServerError, "could not send the challenge", err)
			return
		}
		httpx.JSON(w, http.StatusCreated, map[string]any{
			"challengeId": challengeID, "status": "Pending",
		})
	}
}

// handleRespondToChallenge accepts or declines one.
//
// Accepting is the interesting half: it mints a room and seats both players in
// the same transaction, so a challenge can never be Accepted with no room, nor
// a room exist that nobody agreed to.
func handleRespondToChallenge(d *sql.DB, relay *lichessRelay, accept bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := requireIdentity(d, w, r)
		if id == nil {
			return
		}
		if !canPlay(id.Role) {
			httpx.Error(w, http.StatusForbidden, "not allowed", nil)
			return
		}
		challengeID := r.PathValue("id")

		tx, err := d.Begin()
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not respond", err)
			return
		}
		defer tx.Rollback()

		var from, to string
		var rated, limit, increment int
		// Only the person challenged may answer, and only while it is pending —
		// both conditions in the WHERE clause rather than checked afterwards.
		err = tx.QueryRow(`SELECT from_account_id, to_account_id, rated, clock_limit, clock_increment
		                   FROM game_challenge
		                   WHERE game_challenge_id = ? AND to_account_id = ? AND status = 'Pending'`,
			challengeID, id.UserAccountID).Scan(&from, &to, &rated, &limit, &increment)
		if errors.Is(err, sql.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, "that challenge is no longer waiting for you", nil)
			return
		}
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not respond", err)
			return
		}

		if !accept {
			if _, err := tx.Exec(`UPDATE game_challenge SET status = 'Declined', responded_at = ?
			                      WHERE game_challenge_id = ?`, sqliteNow(), challengeID); err != nil {
				httpx.Error(w, http.StatusInternalServerError, "could not decline", err)
				return
			}
			if err := tx.Commit(); err != nil {
				httpx.Error(w, http.StatusInternalServerError, "could not decline", err)
				return
			}
			httpx.JSON(w, http.StatusOK, map[string]any{"status": "Declined"})
			return
		}

		// The challenger takes White. Somebody has to, and "the one who asked"
		// is the rule a child can predict — a random side would leave both
		// wondering whether the app had made a mistake.
		roomID, err := mintRoom(tx, mintOptions{
			CreatedBy: from, White: from, Black: to,
			Rated: rated == 1, ClockLimit: limit, ClockIncrement: increment,
			Label: "",
		})
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not create the game", err)
			return
		}
		if _, err := tx.Exec(`UPDATE game_challenge
			SET status = 'Accepted', responded_at = ?, game_room_id = ?
			WHERE game_challenge_id = ?`, sqliteNow(), roomID, challengeID); err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not accept", err)
			return
		}
		if err := tx.Commit(); err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not accept", err)
			return
		}
		// Both seats were filled by the accept itself, so nothing else will
		// start the relay — handleJoinRoom triggers it when Black sits down,
		// and nobody sits down here. A rated challenge that never reached
		// Lichess would show a rated badge on a game that is not rated.
		if rated == 1 {
			relay.begin(roomID)
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"status": "Accepted", "gameRoomId": roomID})
	}
}

// handleCancelChallenge withdraws one the caller sent.
func handleCancelChallenge(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := requireIdentity(d, w, r)
		if id == nil {
			return
		}
		res, err := d.Exec(`UPDATE game_challenge SET status = 'Cancelled', responded_at = ?
		                    WHERE game_challenge_id = ? AND from_account_id = ? AND status = 'Pending'`,
			sqliteNow(), r.PathValue("id"), id.UserAccountID)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not cancel", err)
			return
		}
		if n, _ := res.RowsAffected(); n == 0 {
			httpx.Error(w, http.StatusNotFound, "that challenge is no longer yours to cancel", nil)
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"status": "Cancelled"})
	}
}

/* ---- room minting, shared with the staff path ---- */

type mintOptions struct {
	CreatedBy      string
	Label          string
	White, Black   string
	Rated          bool
	ClockLimit     int
	ClockIncrement int
}

// mintRoom inserts a game room, retrying on the unique-code collision.
//
// Factored out of handleCreateRoom so an accepted challenge produces exactly
// the same kind of board as one the console hands out — the only difference
// being that the seats are already filled.
func mintRoom(tx *sql.Tx, o mintOptions) (string, error) {
	rated := 0
	if o.Rated {
		rated = 1
	}
	var seat = func(s string) any {
		if s == "" {
			return nil
		}
		return s
	}
	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		code, err := game.Code()
		if err != nil {
			return "", err
		}
		roomID := newID("gmr")
		_, lastErr = tx.Exec(`INSERT INTO game_room
			(game_room_id, code, label, created_by, fen, white_account_id, black_account_id,
			 lichess_rated, lichess_clock_limit, lichess_clock_increment, status, started_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			roomID, code, strings.TrimSpace(o.Label), o.CreatedBy, game.StartFEN,
			seat(o.White), seat(o.Black), rated, o.ClockLimit, o.ClockIncrement,
			// Both seats are filled the moment it exists, so it opens Active.
			statusFor(o.White, o.Black), startedAt(o.White, o.Black))
		if lastErr == nil {
			return roomID, nil
		}
	}
	return "", lastErr
}

func statusFor(white, black string) string {
	if white != "" && black != "" {
		return "Active"
	}
	return "Open"
}

func startedAt(white, black string) any {
	if white != "" && black != "" {
		return sqliteNow()
	}
	return nil
}

func mountChallenges(mux *http.ServeMux, d *sql.DB, relay *lichessRelay) {
	// Searching names other children, so it needs a session and a budget: it is
	// the one read here that could be used to walk the roster.
	mux.HandleFunc("GET /api/v1/players/search", httpx.RateLimit(60, handleSearchPlayers(d)))

	const p = "/api/v1/challenges"
	mux.HandleFunc("GET "+p, handleListChallenges(d))
	mux.HandleFunc("POST "+p, httpx.RateLimit(20, handleCreateChallenge(d)))
	mux.HandleFunc("POST "+p+"/{id}/accept", handleRespondToChallenge(d, relay, true))
	mux.HandleFunc("POST "+p+"/{id}/decline", handleRespondToChallenge(d, relay, false))
	mux.HandleFunc("DELETE "+p+"/{id}", handleCancelChallenge(d))
}
