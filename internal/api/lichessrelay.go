// Relaying a JTrax game room into a real, rated game on lichess.org.
//
// The students play on the academy's board. Every accepted move is forwarded to
// Lichess using that student's own token, and a stream of the Lichess game runs
// alongside so the room learns about anything that happened over there — a
// flag-fall, a resignation from the Lichess app, the final result.
//
// # Who is authoritative
//
// Split deliberately, because neither answer alone is right:
//
//   - **JTrax decides legality and turn order.** It already replays the move
//     list for every request and it is the board the pupil is looking at.
//     Waiting on a network round trip before showing their own move would make
//     the board feel broken.
//   - **Lichess decides the rated result.** It owns the clock and the rating.
//     If it says the game ended, the game ended, whatever our board thinks.
//
// Both run the same rules over the same move list, so they agree about chess.
// Where they can disagree is time, and that is exactly what the stream is for.
//
// # When they disagree anyway
//
// The relay detaches: the room stops being rated, records why, and the pupils
// finish their game on a board that no longer claims to count. Rolling their
// board back to match Lichess would be worse — it would take back a move a
// child already played.
package api

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/Kusk24/jtrax-backend/internal/lichess"
)

// relayStartTimeout bounds the pairing handshake: challenge, then accept.
const relayStartTimeout = 25 * time.Second

// relayMoveTimeout bounds one forwarded move. Short, because a move that has
// not landed in this long has already lost its race with the clock.
const relayMoveTimeout = 10 * time.Second

type lichessRelay struct {
	db    *sql.DB
	hub   *hub
	oauth *lichessOAuth

	// watching holds one cancel per live game stream. The map is the reason a
	// server can be shut down without leaking a goroutine per game, and the
	// reason a room cannot end up with two streams after a reconnect.
	mu       sync.Mutex
	watching map[string]context.CancelFunc
}

func newLichessRelay(d *sql.DB, h *hub, o *lichessOAuth) *lichessRelay {
	return &lichessRelay{db: d, hub: h, oauth: o, watching: map[string]context.CancelFunc{}}
}

// enabled reports whether rated relay is possible on this deployment at all.
func (rl *lichessRelay) enabled() bool { return rl != nil && rl.oauth.configured() }

/* ---- clock ---- */

// defaultClockLimit and defaultClockIncrement are a school-friendly 15+10.
//
// Long enough that a pupil thinking about a move is not punished for it, and a
// rapid time control rather than classical, so the rating lands in a perf the
// academy already tracks.
const (
	defaultClockLimit     = 900
	defaultClockIncrement = 10
)

// lichessClock validates a time control against what Lichess will accept.
//
// Checked here rather than discovered at pairing time, because by then two
// pupils are sitting at a board waiting for a game that will never start.
func lichessClock(limit, increment int) (int, int, error) {
	if limit == 0 && increment == 0 {
		return defaultClockLimit, defaultClockIncrement, nil
	}
	// Lichess's own list: 0, 15, 30, 45, 60, 90, then any multiple of 60 up to
	// three hours.
	ok := limit == 0 || limit == 15 || limit == 30 || limit == 45 || limit == 60 || limit == 90 ||
		(limit%60 == 0 && limit >= 60 && limit <= 10800)
	if !ok {
		return 0, 0, errors.New("that clock is not one Lichess accepts")
	}
	if increment < 0 || increment > 60 {
		return 0, 0, errors.New("the increment must be between 0 and 60 seconds")
	}
	// A game with no time at all cannot be played, let alone rated.
	if limit == 0 && increment == 0 {
		return 0, 0, errors.New("a rated game needs a clock")
	}
	return limit, increment, nil
}

/* ---- eligibility ---- */

// seatTokens resolves both seats to Lichess usernames and tokens.
//
// A rated game needs a token from each side: one to issue the challenge and one
// to accept it. That is a product constraint rather than something to engineer
// around — the academy cannot play a pupil's account without the pupil's grant.
func (rl *lichessRelay) seatTokens(roomID string) (white, black relaySeat, err error) {
	var whiteAcct, blackAcct sql.NullString
	if err = rl.db.QueryRow(`SELECT white_account_id, black_account_id FROM game_room
	                         WHERE game_room_id = ?`, roomID).Scan(&whiteAcct, &blackAcct); err != nil {
		return
	}
	if !whiteAcct.Valid || !blackAcct.Valid {
		err = errors.New("lichess relay: both seats must be filled")
		return
	}
	if white, err = rl.seatFor(whiteAcct.String); err != nil {
		return
	}
	black, err = rl.seatFor(blackAcct.String)
	return
}

type relaySeat struct {
	studentID string
	username  string
	token     string
}

func (rl *lichessRelay) seatFor(accountID string) (relaySeat, error) {
	var studentID string
	// Only a student has a Lichess link. A teacher sitting down for a lesson
	// has no student row, so a teacher-versus-pupil game simply cannot be
	// rated — which is correct: a coaching game should not move a child's
	// rating anyway.
	err := rl.db.QueryRow(`SELECT student_id FROM student WHERE user_account_id = ?`, accountID).Scan(&studentID)
	if errors.Is(err, sql.ErrNoRows) {
		return relaySeat{}, errors.New("lichess relay: that seat is not a student")
	}
	if err != nil {
		return relaySeat{}, err
	}
	username, token, err := rl.oauth.playToken(studentID)
	if err != nil {
		return relaySeat{}, err
	}
	return relaySeat{studentID: studentID, username: username, token: token}, nil
}

/* ---- starting ---- */

// begin pairs the two students on Lichess and starts following the game.
//
// Called when the second seat fills. Runs in the caller's goroutine so a room
// that cannot be rated says so immediately rather than appearing rated for a
// second and then changing its mind.
func (rl *lichessRelay) begin(roomID string) {
	if !rl.enabled() {
		rl.detach(roomID, "notConfigured")
		return
	}
	white, black, err := rl.seatTokens(roomID)
	if err != nil {
		reason := "noPlayAccess"
		if errors.Is(err, errTokenExpired) {
			reason = "tokenExpired"
		}
		log.Printf("lichess relay: room %s cannot be rated: %v", roomID, err)
		rl.detach(roomID, reason)
		return
	}

	var limit, increment int
	if err := rl.db.QueryRow(`SELECT lichess_clock_limit, lichess_clock_increment
	                          FROM game_room WHERE game_room_id = ?`, roomID).
		Scan(&limit, &increment); err != nil {
		rl.detach(roomID, "failed")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), relayStartTimeout)
	defer cancel()

	// White challenges, so white gets the white pieces on Lichess too. Seats
	// matching across the two boards is not cosmetic: every relayed move is
	// posted with a specific player's token, and a swap would make every one of
	// them illegal.
	ch, err := rl.oauth.client.Challenge(ctx, white.token, black.username, lichess.ChallengeParams{
		Rated: true, ClockLimit: limit, ClockIncrement: increment, Color: "white",
	})
	if err != nil {
		log.Printf("lichess relay: challenge for room %s: %v", roomID, err)
		rl.detach(roomID, "challengeFailed")
		return
	}
	if err := rl.oauth.client.AcceptChallenge(ctx, black.token, ch.ID); err != nil {
		log.Printf("lichess relay: accept for room %s: %v", roomID, err)
		// Leaving a challenge pending would sit in the opponent's Lichess inbox
		// as an invitation from a game that no longer exists.
		if cerr := rl.oauth.client.CancelChallenge(ctx, white.token, ch.ID); cerr != nil {
			log.Printf("lichess relay: cancelling orphaned challenge %s: %v", ch.ID, cerr)
		}
		rl.detach(roomID, "opponentDeclined")
		return
	}

	if _, err := rl.db.Exec(`UPDATE game_room SET lichess_game_id = ?, lichess_status = 'started',
	                         lichess_detached_reason = NULL WHERE game_room_id = ?`,
		ch.ID, roomID); err != nil {
		log.Printf("lichess relay: storing game id for room %s: %v", roomID, err)
		rl.detach(roomID, "failed")
		return
	}
	publishRoom(rl.db, rl.hub, roomID)
	rl.watch(roomID, ch.ID, white.token)
}

/* ---- surviving a restart ---- */

// resume re-attaches a stream to every rated game still in play.
//
// The watchers live in memory, so a deploy in the middle of a lesson used to
// leave two pupils playing a rated game nobody was listening to: the result
// would land on Lichess and never on the room, which would sit Active forever.
// This is the free tier — the server restarts whenever it is redeployed or wakes
// from sleep — so that is a normal Tuesday, not an edge case.
//
// Reconnecting to a game that already finished is safe and is in fact the point:
// Lichess replays the full state and closes the stream, which is exactly the
// reconciliation the room missed.
func (rl *lichessRelay) resume() {
	if !rl.enabled() {
		return
	}
	rows, err := rl.db.Query(`SELECT game_room_id, lichess_game_id FROM game_room
	                          WHERE status = 'Active' AND lichess_rated = 1
	                            AND lichess_game_id IS NOT NULL AND lichess_game_id <> ''`)
	if err != nil {
		log.Printf("lichess relay: could not list games to resume: %v", err)
		return
	}
	defer rows.Close()

	type pending struct{ roomID, gameID string }
	var todo []pending
	for rows.Next() {
		var p pending
		if err := rows.Scan(&p.roomID, &p.gameID); err != nil {
			log.Printf("lichess relay: reading a game to resume: %v", err)
			continue
		}
		todo = append(todo, p)
	}
	if len(todo) == 0 {
		return
	}

	// Off the boot path: this makes one outbound connection per live game and
	// must not hold up the server answering requests.
	go func() {
		for _, p := range todo {
			white, _, err := rl.seatTokens(p.roomID)
			if err != nil {
				log.Printf("lichess relay: cannot resume room %s: %v", p.roomID, err)
				rl.detach(p.roomID, "tokenExpired")
				continue
			}
			log.Printf("lichess relay: resuming room %s (lichess game %s)", p.roomID, p.gameID)
			rl.watch(p.roomID, p.gameID, white.token)
		}
	}()
}

/* ---- forwarding moves ---- */

// forward sends one accepted move on to Lichess.
//
// Asynchronous on purpose. The pupil's move is already on their board and in
// our database by the time this runs; making them wait on lichess.org before
// their own piece moves is the thing that made "instant" worth asking for.
func (rl *lichessRelay) forward(roomID, seat, uci string) {
	if !rl.enabled() {
		return
	}
	var gameID sql.NullString
	var rated int
	if err := rl.db.QueryRow(`SELECT lichess_game_id, lichess_rated FROM game_room
	                          WHERE game_room_id = ?`, roomID).Scan(&gameID, &rated); err != nil {
		return
	}
	if rated != 1 || !gameID.Valid || gameID.String == "" {
		return
	}
	go func() {
		white, black, err := rl.seatTokens(roomID)
		if err != nil {
			rl.detach(roomID, "noPlayAccess")
			return
		}
		// The move must be posted with the token of the player who made it;
		// Lichess rejects it otherwise, and rightly.
		mover := white
		if seat == "Black" {
			mover = black
		}
		ctx, cancel := context.WithTimeout(context.Background(), relayMoveTimeout)
		defer cancel()
		if err := rl.oauth.client.Move(ctx, mover.token, gameID.String, uci); err != nil {
			if errors.Is(err, lichess.ErrMoveRejected) {
				// The boards have diverged. Almost always because Lichess has
				// already ended the game — a flag-fall we have not yet seen on
				// the stream — so this is reported, not retried.
				log.Printf("lichess relay: room %s move %s rejected: %v", roomID, uci, err)
				rl.detach(roomID, "moveRejected")
				return
			}
			log.Printf("lichess relay: room %s move %s: %v", roomID, uci, err)
			rl.detach(roomID, "unreachable")
		}
	}()
}

/* ---- following the game ---- */

// watch follows the Lichess game until it ends.
//
// One goroutine per live rated room. The stream sends the whole move list on
// every update rather than a delta, so a dropped connection costs nothing to
// recover from: the next message is a complete picture.
func (rl *lichessRelay) watch(roomID, gameID, token string) {
	rl.mu.Lock()
	if _, already := rl.watching[roomID]; already {
		rl.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	rl.watching[roomID] = cancel
	rl.mu.Unlock()

	go func() {
		defer func() {
			rl.mu.Lock()
			delete(rl.watching, roomID)
			rl.mu.Unlock()
			cancel()
		}()
		err := rl.oauth.client.StreamGame(ctx, token, gameID, func(st lichess.GameState) {
			rl.onState(roomID, st)
		})
		if err != nil && ctx.Err() == nil {
			log.Printf("lichess relay: stream for room %s ended: %v", roomID, err)
		}
	}()
}

// onState records what Lichess says about the game.
func (rl *lichessRelay) onState(roomID string, st lichess.GameState) {
	if _, err := rl.db.Exec(`UPDATE game_room SET lichess_status = ? WHERE game_room_id = ?`,
		st.Status, roomID); err != nil {
		log.Printf("lichess relay: recording status for room %s: %v", roomID, err)
		return
	}
	if !lichess.Finished(st.Status) {
		// Mid-game updates still reach the board, because the clock is on them
		// and a pupil needs to see it moving.
		publishRoom(rl.db, rl.hub, roomID)
		return
	}

	// Lichess has the last word on a rated game's result.
	//
	// nil rather than "" for a game with no result: the column is constrained
	// to the three real scores, so an empty string is rejected outright and an
	// aborted game would fail to record at all.
	var result any
	if r := relayResult(st); r != "" {
		result = r
	}
	if _, err := rl.db.Exec(`UPDATE game_room
	                         SET status = 'Finished', result = COALESCE(result, ?),
	                             result_reason = COALESCE(result_reason, ?),
	                             ended_at = COALESCE(ended_at, datetime('now'))
	                         WHERE game_room_id = ? AND status = 'Active'`,
		result, "lichess:"+st.Status, roomID); err != nil {
		log.Printf("lichess relay: finishing room %s: %v", roomID, err)
	}
	publishRoom(rl.db, rl.hub, roomID)
}

// relayResult maps a Lichess winner onto the house result strings.
func relayResult(st lichess.GameState) string {
	switch strings.ToLower(st.Winner) {
	case "white":
		return "1-0"
	case "black":
		return "0-1"
	}
	// No winner on a finished game means a draw — by agreement, stalemate,
	// repetition or material. Aborted games have no result at all and must not
	// be recorded as a draw, which would be a half point nobody played for.
	if st.Status == "aborted" || st.Status == "noStart" {
		return ""
	}
	return "1/2-1/2"
}

/* ---- detaching ---- */

// detach marks a room as no longer counting, and says why.
//
// Never silent. A pupil who was told their game was rated has to be told when
// that stops being true, while they can still see the board.
func (rl *lichessRelay) detach(roomID, reason string) {
	if _, err := rl.db.Exec(`UPDATE game_room SET lichess_rated = 0, lichess_detached_reason = ?
	                         WHERE game_room_id = ?`, reason, roomID); err != nil {
		log.Printf("lichess relay: detaching room %s: %v", roomID, err)
		return
	}
	rl.stop(roomID)
	publishRoom(rl.db, rl.hub, roomID)
}

// stop ends the stream for one room.
func (rl *lichessRelay) stop(roomID string) {
	rl.mu.Lock()
	cancel := rl.watching[roomID]
	delete(rl.watching, roomID)
	rl.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// abandon ends the Lichess side when staff pull the room.
//
// Aborts rather than resigns where Lichess still allows it: a room cancelled
// before anyone had a chance to play should not put a loss on a child's record.
// Once a game is past the point of abortion Lichess refuses, and the game is
// left for the players to finish or time out — the alternative would be
// resigning on behalf of a pupil who did no such thing.
func (rl *lichessRelay) abandon(roomID string) {
	if !rl.enabled() {
		return
	}
	var gameID sql.NullString
	if err := rl.db.QueryRow(`SELECT lichess_game_id FROM game_room WHERE game_room_id = ?`,
		roomID).Scan(&gameID); err != nil || !gameID.Valid || gameID.String == "" {
		rl.stop(roomID)
		return
	}
	white, _, err := rl.seatTokens(roomID)
	rl.stop(roomID)
	if err != nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), relayMoveTimeout)
		defer cancel()
		if err := rl.oauth.client.Abort(ctx, white.token, gameID.String); err != nil {
			log.Printf("lichess relay: aborting room %s: %v", roomID, err)
		}
	}()
}

/* ---- resignation ---- */

// resign forwards a resignation so the rating moves.
//
// Without this a pupil resigning in JTrax would leave the Lichess game running
// until it timed out, and the rating would eventually move for the wrong
// reason, minutes later.
func (rl *lichessRelay) resign(roomID, seat string) {
	if !rl.enabled() {
		return
	}
	var gameID sql.NullString
	var rated int
	if err := rl.db.QueryRow(`SELECT lichess_game_id, lichess_rated FROM game_room
	                          WHERE game_room_id = ?`, roomID).Scan(&gameID, &rated); err != nil {
		return
	}
	if rated != 1 || !gameID.Valid || gameID.String == "" {
		return
	}
	white, black, err := rl.seatTokens(roomID)
	if err != nil {
		return
	}
	token := white.token
	if seat == "Black" {
		token = black.token
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), relayMoveTimeout)
		defer cancel()
		if err := rl.oauth.client.Resign(ctx, token, gameID.String); err != nil {
			log.Printf("lichess relay: resigning room %s: %v", roomID, err)
		}
	}()
}
