package api_test

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

/* A stand-in for the play half of lichess.org.

   Unlike the OAuth stub this one issues a *different* token per student, which
   is the only way to test the property the whole relay rests on: a move is
   posted with the token of the player who made it, and never the other one. */

type playStub struct {
	mu  sync.Mutex
	srv *httptest.Server

	// names maps a token back to the username in its registered case. Lichess
	// preserves case even though it matches case-insensitively, and a stub that
	// lowercased everything would hide a real bug in the challenge path.
	names map[string]string

	challenges []stubChallenge
	moves      []stubMove
	resigns    []string
	aborts     []string
	// streams lets a test drive what Lichess says about a live game.
	streams map[string]chan string
	// done releases every open stream handler at the end of a test.
	//
	// Needed because httptest's Close waits for outstanding requests, and a
	// game stream is outstanding for as long as the game lasts. Without this a
	// test that finishes while a game is still live hangs on teardown rather
	// than failing.
	done chan struct{}
	// declineAccept makes the opponent refuse, to exercise the unhappy path.
	declineAccept bool
}

type stubChallenge struct{ Token, Opponent, Rated, Clock string }
type stubMove struct{ Token, GameID, UCI string }

func newPlayStub(t *testing.T) *playStub {
	s := &playStub{streams: map[string]chan string{}, names: map[string]string{}, done: make(chan struct{})}
	mux := http.NewServeMux()

	// ---- OAuth: the code names the account, so each student gets their own
	// token and the test can tell them apart.
	mux.HandleFunc("POST /api/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		name := strings.TrimPrefix(r.PostForm.Get("code"), "grant:")
		token := "tok_" + strings.ToLower(name)
		s.mu.Lock()
		s.names[token] = name
		s.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token_type": "Bearer", "expires_in": 31536000, "access_token": token,
		})
	})
	mux.HandleFunc("GET /api/account", func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		s.mu.Lock()
		name := s.names[token]
		s.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"id": strings.ToLower(name), "username": name})
	})
	mux.HandleFunc("DELETE /api/token", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	// ---- pairing
	mux.HandleFunc("POST /api/challenge/{username}", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		opponent := r.PathValue("username")
		s.mu.Lock()
		s.challenges = append(s.challenges, stubChallenge{
			Token:    strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "),
			Opponent: opponent,
			Rated:    r.PostForm.Get("rated"),
			Clock:    r.PostForm.Get("clock.limit") + "+" + r.PostForm.Get("clock.increment"),
		})
		id := fmt.Sprintf("game%d", len(s.challenges))
		s.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": id, "url": "https://lichess.org/" + id,
		})
	})
	mux.HandleFunc("POST /api/challenge/{id}/accept", func(w http.ResponseWriter, _ *http.Request) {
		s.mu.Lock()
		decline := s.declineAccept
		s.mu.Unlock()
		if decline {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"challenge not found"}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	mux.HandleFunc("POST /api/challenge/{id}/cancel", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	})

	// ---- play
	mux.HandleFunc("POST /api/board/game/{id}/move/{uci}", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.moves = append(s.moves, stubMove{
			Token:  strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "),
			GameID: r.PathValue("id"), UCI: r.PathValue("uci"),
		})
		s.mu.Unlock()
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	mux.HandleFunc("POST /api/board/game/{id}/resign", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.resigns = append(s.resigns, r.PathValue("id"))
		s.mu.Unlock()
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	mux.HandleFunc("POST /api/board/game/{id}/abort", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.aborts = append(s.aborts, r.PathValue("id"))
		s.mu.Unlock()
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	mux.HandleFunc("GET /api/board/game/stream/{id}", func(w http.ResponseWriter, r *http.Request) {
		ch := s.streamFor(r.PathValue("id"))
		flusher, _ := w.(http.Flusher)
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = w.Write([]byte(`{"type":"gameFull","id":"` + r.PathValue("id") +
			`","state":{"type":"gameState","moves":"","status":"started"}}` + "\n"))
		if flusher != nil {
			flusher.Flush()
		}
		for {
			select {
			case line, ok := <-ch:
				if !ok {
					return
				}
				_, _ = w.Write([]byte(line + "\n"))
				if flusher != nil {
					flusher.Flush()
				}
			case <-r.Context().Done():
				return
			case <-s.done:
				return
			}
		}
	})

	// ---- reads the rating sync still makes
	mux.HandleFunc("GET /api/user/{name}", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": strings.ToLower(name), "username": name,
			"profile": map[string]any{"bio": ""},
			"perfs":   map[string]any{"rapid": map[string]any{"rating": 1400, "games": 30}},
		})
	})
	mux.HandleFunc("POST /api/users", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[]`))
	})

	s.srv = httptest.NewServer(mux)
	t.Cleanup(s.srv.Close)
	// Registered after Close so it runs *before* it: cleanups are LIFO, and
	// Close cannot return until the streams it is waiting on have let go.
	t.Cleanup(func() { close(s.done) })
	return s
}

func (s *playStub) streamFor(id string) chan string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.streams[id] == nil {
		s.streams[id] = make(chan string, 8)
	}
	return s.streams[id]
}

// push sends one game-state line to a live stream. The channel is buffered, so
// a state pushed while nobody is connected waits for whoever connects next —
// which is exactly how "the game ended while the server was down" is expressed.
func (s *playStub) push(id, line string) { s.streamFor(id) <- line }

// closeStreams drops every open stream, as a server going away would.
//
// The handlers return, the watchers on the other end see the stream end and
// their goroutines exit — so the next server to connect is genuinely the only
// listener, rather than racing a leftover one from before the "restart".
func (s *playStub) closeStreams() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, ch := range s.streams {
		close(ch)
		delete(s.streams, id)
	}
}

func (s *playStub) challengeList() []stubChallenge {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]stubChallenge(nil), s.challenges...)
}

func (s *playStub) moveList() []stubMove {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]stubMove(nil), s.moves...)
}

func (s *playStub) resignList() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.resigns...)
}

func (s *playStub) abortList() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.aborts...)
}

func newPlayServer(t *testing.T) (*client, *playStub) {
	t.Helper()
	stub := newPlayStub(t)
	t.Setenv("LICHESS_API_BASE", stub.srv.URL)
	t.Setenv("PUBLIC_API_URL", "https://api.test")
	t.Setenv("APP_URL", "https://portal.test")
	t.Setenv("LICHESS_TOKEN_KEY", base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef")))
	return &client{t: t, srv: newServer(t)}, stub
}

// grantPlay runs a whole OAuth flow for one student.
func grantPlay(t *testing.T, base *client, email, lichessName string) *client {
	t.Helper()
	c := asStudent(t, base, email)
	state, _ := startFlow(t, c, nil)
	if code := completeCallback(t, base, state, "grant:"+lichessName); code != http.StatusOK &&
		code != http.StatusSeeOther {
		t.Fatalf("granting play for %s: status %d", email, code)
	}
	if status, obj, _ := c.do("GET", "/api/v1/lichess/play-status", nil); status != 200 || obj["canPlay"] != true {
		t.Fatalf("%s has no play access after granting: %v", email, obj)
	}
	return c
}

// pairInRoom creates a rated room and seats both students.
func pairInRoom(t *testing.T, base *client, white, black *client) string {
	t.Helper()
	admin := asStudent(t, base, "admin@jca.ac.th")
	status, room, _ := admin.do("POST", "/api/v1/game-rooms", map[string]any{
		"label": "Rated test", "lichessRated": true, "clockLimit": 600, "clockIncrement": 5,
	})
	if status != 201 {
		t.Fatalf("create rated room: %d (%v)", status, room)
	}
	code := room["code"].(string)
	roomID := room["gameRoomId"].(string)

	if s, obj, _ := white.do("POST", "/api/v1/game-rooms/join", map[string]string{"code": code}); s != 200 {
		t.Fatalf("white join: %d (%v)", s, obj)
	}
	if s, obj, _ := black.do("POST", "/api/v1/game-rooms/join", map[string]string{"code": code}); s != 200 {
		t.Fatalf("black join: %d (%v)", s, obj)
	}
	return roomID
}

/* ---- pairing ---- */

func TestRatedRoomPairsBothStudentsOnLichess(t *testing.T) {
	base, stub := newPlayServer(t)
	penny := grantPlay(t, base, "penny@jca.ac.th", "PennyPlays")
	uri := grantPlay(t, base, "uri@jca.ac.th", "UriPlays")

	roomID := pairInRoom(t, base, penny, uri)

	chals := stub.challengeList()
	if len(chals) != 1 {
		t.Fatalf("expected exactly one challenge, got %d", len(chals))
	}
	c := chals[0]
	// White challenges black. Seats matching across the two boards is not
	// cosmetic: every relayed move is posted with a specific player's token.
	if c.Token != "tok_pennyplays" {
		t.Errorf("challenge issued with %q, want white's token", c.Token)
	}
	if c.Opponent != "UriPlays" {
		t.Errorf("challenged %q, want black", c.Opponent)
	}
	if c.Rated != "true" {
		t.Errorf("rated = %q — the entire point is that it counts", c.Rated)
	}
	if c.Clock != "600+5" {
		t.Errorf("clock = %q, want the room's 600+5", c.Clock)
	}

	status, obj, _ := penny.do("GET", "/api/v1/game-rooms/"+roomID, nil)
	if status != 200 {
		t.Fatalf("get room: %d", status)
	}
	room := obj["room"].(map[string]any)
	if room["lichessRated"] != true {
		t.Errorf("room is not marked rated: %v", room)
	}
	if room["lichessGameId"] != "game1" {
		t.Errorf("lichessGameId = %v", room["lichessGameId"])
	}
}

// Both pupils must have granted play access. One who has not is not a failure
// to hide — the room says it is not rated, and why.
func TestRoomDetachesWhenAPlayerHasNoPlayAccess(t *testing.T) {
	base, stub := newPlayServer(t)
	penny := grantPlay(t, base, "penny@jca.ac.th", "PennyPlays")
	uri := asStudent(t, base, "uri@jca.ac.th") // never granted

	roomID := pairInRoom(t, base, penny, uri)

	if n := len(stub.challengeList()); n != 0 {
		t.Errorf("challenged Lichess anyway (%d times)", n)
	}
	room := roomOf(t, penny, roomID)
	if room["lichessRated"] != false {
		t.Errorf("room still claims to be rated: %v", room)
	}
	if room["lichessDetachedReason"] != "noPlayAccess" {
		t.Errorf("detach reason = %v, want noPlayAccess", room["lichessDetachedReason"])
	}
	// The game itself must still be playable. Losing the rating is not losing
	// the lesson.
	if room["status"] != "Active" {
		t.Errorf("status = %v, want the board to still be in play", room["status"])
	}
}

func TestDeclinedChallengeDetachesTheRoom(t *testing.T) {
	base, stub := newPlayServer(t)
	stub.mu.Lock()
	stub.declineAccept = true
	stub.mu.Unlock()

	penny := grantPlay(t, base, "penny@jca.ac.th", "PennyPlays")
	uri := grantPlay(t, base, "uri@jca.ac.th", "UriPlays")
	roomID := pairInRoom(t, base, penny, uri)

	room := roomOf(t, penny, roomID)
	if room["lichessRated"] != false || room["lichessDetachedReason"] != "opponentDeclined" {
		t.Errorf("room = %v, want detached with opponentDeclined", room)
	}
}

/* ---- moves ---- */

// The property the whole relay rests on: each move goes up with the token of
// the player who actually made it.
func TestRelayForwardsMovesWithTheMoversOwnToken(t *testing.T) {
	base, stub := newPlayServer(t)
	penny := grantPlay(t, base, "penny@jca.ac.th", "PennyPlays")
	uri := grantPlay(t, base, "uri@jca.ac.th", "UriPlays")
	roomID := pairInRoom(t, base, penny, uri)

	if s, obj, _ := penny.do("POST", "/api/v1/game-rooms/"+roomID+"/moves",
		map[string]string{"move": "e2e4"}); s != 200 {
		t.Fatalf("white move: %d (%v)", s, obj)
	}
	if s, obj, _ := uri.do("POST", "/api/v1/game-rooms/"+roomID+"/moves",
		map[string]string{"move": "e7e5"}); s != 200 {
		t.Fatalf("black move: %d (%v)", s, obj)
	}

	moves := waitForMoves(t, stub, 2)
	want := []stubMove{
		{Token: "tok_pennyplays", GameID: "game1", UCI: "e2e4"},
		{Token: "tok_uriplays", GameID: "game1", UCI: "e7e5"},
	}
	for i, w := range want {
		if moves[i] != w {
			t.Errorf("move %d = %+v, want %+v", i, moves[i], w)
		}
	}
}

// Forwarding is asynchronous on purpose: the pupil's own piece must not wait on
// a round trip to lichess.org. So the move response has to come back before the
// relay has finished, and the board must not block on it.
func TestMoveResponseDoesNotWaitOnLichess(t *testing.T) {
	base, _ := newPlayServer(t)
	penny := grantPlay(t, base, "penny@jca.ac.th", "PennyPlays")
	uri := grantPlay(t, base, "uri@jca.ac.th", "UriPlays")
	roomID := pairInRoom(t, base, penny, uri)

	start := time.Now()
	if s, _, _ := penny.do("POST", "/api/v1/game-rooms/"+roomID+"/moves",
		map[string]string{"move": "e2e4"}); s != 200 {
		t.Fatalf("move: %d", s)
	}
	// Generous: the assertion is "not a network round trip's worth of blocking",
	// not a benchmark.
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("the move took %v — it is waiting on the relay", elapsed)
	}
}

/* ---- results ---- */

// Lichess owns the clock and therefore the result. When it says the game is
// over, the room is over, whatever our board thinks.
func TestLichessResultFinishesTheRoom(t *testing.T) {
	base, stub := newPlayServer(t)
	penny := grantPlay(t, base, "penny@jca.ac.th", "PennyPlays")
	uri := grantPlay(t, base, "uri@jca.ac.th", "UriPlays")
	roomID := pairInRoom(t, base, penny, uri)

	// A flag-fall: nothing our own board would ever produce, because it has no
	// clock of its own.
	stub.push("game1", `{"type":"gameState","moves":"e2e4","status":"outoftime","winner":"black"}`)

	room := waitForRoom(t, penny, roomID, func(r map[string]any) bool {
		return r["status"] == "Finished"
	})
	if room["result"] != "0-1" {
		t.Errorf("result = %v, want 0-1", room["result"])
	}
	if room["lichessStatus"] != "outoftime" {
		t.Errorf("lichessStatus = %v, want outoftime", room["lichessStatus"])
	}
	if !strings.Contains(fmt.Sprint(room["resultReason"]), "outoftime") {
		t.Errorf("resultReason = %v — it should say Lichess ended it", room["resultReason"])
	}
}

// An aborted game is not a draw. Recording one would put half a point on two
// children's records that neither of them played for.
func TestAbortedLichessGameIsNotADraw(t *testing.T) {
	base, stub := newPlayServer(t)
	penny := grantPlay(t, base, "penny@jca.ac.th", "PennyPlays")
	uri := grantPlay(t, base, "uri@jca.ac.th", "UriPlays")
	roomID := pairInRoom(t, base, penny, uri)

	stub.push("game1", `{"type":"gameState","moves":"","status":"aborted"}`)

	room := waitForRoom(t, penny, roomID, func(r map[string]any) bool {
		return r["lichessStatus"] == "aborted"
	})
	if res := fmt.Sprint(room["result"]); res == "1/2-1/2" {
		t.Errorf("an aborted game was recorded as a draw")
	}
}

// Resigning here has to resign there, or the rating moves minutes later when
// the abandoned Lichess game finally times out.
func TestResigningForwardsToLichess(t *testing.T) {
	base, stub := newPlayServer(t)
	penny := grantPlay(t, base, "penny@jca.ac.th", "PennyPlays")
	uri := grantPlay(t, base, "uri@jca.ac.th", "UriPlays")
	roomID := pairInRoom(t, base, penny, uri)

	if s, _, _ := penny.do("POST", "/api/v1/game-rooms/"+roomID+"/resign", nil); s != 200 {
		t.Fatalf("resign: %d", s)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(stub.resignList()) > 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the resignation never reached Lichess")
}

// A cancelled room must not leave a live game on two children's accounts.
func TestCancellingARoomAbortsTheLichessGame(t *testing.T) {
	base, stub := newPlayServer(t)
	penny := grantPlay(t, base, "penny@jca.ac.th", "PennyPlays")
	uri := grantPlay(t, base, "uri@jca.ac.th", "UriPlays")
	roomID := pairInRoom(t, base, penny, uri)

	admin := asStudent(t, base, "admin@jca.ac.th")
	if s, _, _ := admin.do("DELETE", "/api/v1/game-rooms/"+roomID, nil); s != 200 {
		t.Fatalf("cancel: %d", s)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(stub.abortList()) > 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("cancelling the room left the Lichess game running")
}

/* ---- surviving a restart ---- */

// A rated game in play when the server stops must be picked up again when it
// comes back. On the free tier this happens on every deploy and every wake from
// sleep, so a room left Active forever is not a rare edge case.
func TestResumePicksUpAGameThatEndedWhileTheServerWasDown(t *testing.T) {
	stub := newPlayStub(t)
	t.Setenv("LICHESS_API_BASE", stub.srv.URL)
	t.Setenv("PUBLIC_API_URL", "https://api.test")
	t.Setenv("APP_URL", "https://portal.test")
	t.Setenv("LICHESS_TOKEN_KEY", base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef")))

	// One database, two servers in sequence: the same shape as a redeploy.
	d := newDB(t)
	first := &client{t: t, srv: newServerOn(t, d)}
	penny := grantPlay(t, first, "penny@jca.ac.th", "PennyPlays")
	uri := grantPlay(t, first, "uri@jca.ac.th", "UriPlays")
	roomID := pairInRoom(t, first, penny, uri)

	// The game ends on Lichess while nobody here is listening.
	stub.closeStreams()
	stub.push("game1", `{"type":"gameState","moves":"e2e4 e7e5 d1h5 b8c6 f1c4 g8f6 h5f7","status":"mate","winner":"white"}`)

	// Restart.
	second := &client{t: t, srv: newServerOn(t, d)}
	pennyAgain := asStudent(t, second, "penny@jca.ac.th")

	room := waitForRoom(t, pennyAgain, roomID, func(r map[string]any) bool {
		return r["status"] == "Finished"
	})
	if room["result"] != "1-0" {
		t.Errorf("result = %v, want 1-0 recovered from Lichess", room["result"])
	}
}

/* ---- unrated rooms are untouched ---- */

func TestUnratedRoomNeverTouchesLichess(t *testing.T) {
	base, stub := newPlayServer(t)
	penny := grantPlay(t, base, "penny@jca.ac.th", "PennyPlays")
	uri := grantPlay(t, base, "uri@jca.ac.th", "UriPlays")

	admin := asStudent(t, base, "admin@jca.ac.th")
	_, room, _ := admin.do("POST", "/api/v1/game-rooms", map[string]any{"label": "Practice"})
	code := room["code"].(string)
	roomID := room["gameRoomId"].(string)
	penny.do("POST", "/api/v1/game-rooms/join", map[string]string{"code": code})
	uri.do("POST", "/api/v1/game-rooms/join", map[string]string{"code": code})
	penny.do("POST", "/api/v1/game-rooms/"+roomID+"/moves", map[string]string{"move": "e2e4"})

	time.Sleep(300 * time.Millisecond)
	if n := len(stub.challengeList()); n != 0 {
		t.Errorf("an unrated room created %d Lichess challenges", n)
	}
	if n := len(stub.moveList()); n != 0 {
		t.Errorf("an unrated room relayed %d moves", n)
	}
}

/* ---- clock validation ---- */

func TestRatedRoomRejectsAClockLichessWillNotAccept(t *testing.T) {
	base, _ := newPlayServer(t)
	admin := asStudent(t, base, "admin@jca.ac.th")

	// 100 seconds is neither on Lichess's short list nor a multiple of 60.
	// Finding that out at pairing time would mean two pupils sitting at a board
	// waiting for a game that never starts.
	if s, _, _ := admin.do("POST", "/api/v1/game-rooms", map[string]any{
		"lichessRated": true, "clockLimit": 100, "clockIncrement": 5,
	}); s != 400 {
		t.Errorf("status %d for a 100-second clock, want 400", s)
	}
	// 7 minutes, by contrast, is a multiple of 60 and perfectly legal.
	if s, _, _ := admin.do("POST", "/api/v1/game-rooms", map[string]any{
		"lichessRated": true, "clockLimit": 420, "clockIncrement": 5,
	}); s != 201 {
		t.Errorf("status %d for a 7-minute clock, want 201", s)
	}
	if s, _, _ := admin.do("POST", "/api/v1/game-rooms", map[string]any{
		"lichessRated": true, "clockLimit": 600, "clockIncrement": 90,
	}); s != 400 {
		t.Errorf("status %d for a 90s increment, want 400", s)
	}
	if s, _, _ := admin.do("POST", "/api/v1/game-rooms", map[string]any{
		"lichessRated": true, "clockLimit": 600, "clockIncrement": 5,
	}); s != 201 {
		t.Errorf("status %d for a valid 10+5, want 201", s)
	}
}

/* ---- helpers ---- */

// roomOf unwraps the room from the endpoint's envelope.
func roomOf(t *testing.T, c *client, roomID string) map[string]any {
	t.Helper()
	_, obj, _ := c.do("GET", "/api/v1/game-rooms/"+url.PathEscape(roomID), nil)
	room, _ := obj["room"].(map[string]any)
	if room == nil {
		return map[string]any{}
	}
	return room
}

func waitForMoves(t *testing.T, stub *playStub, n int) []stubMove {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if moves := stub.moveList(); len(moves) >= n {
			return moves
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("only %d of %d moves reached Lichess", len(stub.moveList()), n)
	return nil
}

func waitForRoom(t *testing.T, c *client, roomID string, done func(map[string]any) bool) map[string]any {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var room map[string]any
	for time.Now().Before(deadline) {
		room = roomOf(t, c, roomID)
		if done(room) {
			return room
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("room never reached the expected state: %v", room)
	return nil
}
