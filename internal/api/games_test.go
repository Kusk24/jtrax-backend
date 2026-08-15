package api_test

import (
	"fmt"
	"net/http/httptest"
	"sync"
	"testing"
)

// Fool's mate, the shortest game that ends in a result — two moves each.
var foolsMate = []string{"f2f3", "e7e5", "g2g4", "d8h4"}

type table struct {
	t     *testing.T
	srv   *httptest.Server
	admin *client
	white *client
	black *client
	id    string
	code  string
}

// openRoom mints a room as the admin and returns the helpers for it.
func openRoom(t *testing.T) *table {
	t.Helper()
	srv := newServer(t)
	admin := &client{t: t, srv: srv}
	admin.login("admin@jca.ac.th")

	status, room, _ := admin.do("POST", "/api/v1/game-rooms", map[string]string{"label": "Friday club"})
	if status != 201 {
		t.Fatalf("create room: status %d (%v)", status, room)
	}
	code, _ := room["code"].(string)
	if code == "" {
		t.Fatal("create room returned no code")
	}
	return &table{t: t, srv: srv, admin: admin, id: room["gameRoomId"].(string), code: code}
}

// seat logs a player in and joins them to the room, returning their colour.
func (tb *table) seat(email string) (*client, string) {
	tb.t.Helper()
	c := &client{t: tb.t, srv: tb.srv}
	c.login(email)
	status, obj, _ := c.do("POST", "/api/v1/game-rooms/join", map[string]string{"code": tb.code})
	if status != 200 {
		tb.t.Fatalf("join as %s: status %d (%v)", email, status, obj)
	}
	return c, obj["seat"].(string)
}

// bothSeats fills the room with the two seeded students.
func (tb *table) bothSeats() *table {
	tb.t.Helper()
	penny, pennySeat := tb.seat("penny@jca.ac.th")
	uri, _ := tb.seat("uri@jca.ac.th")
	if pennySeat == "White" {
		tb.white, tb.black = penny, uri
	} else {
		tb.white, tb.black = uri, penny
	}
	return tb
}

func (tb *table) move(c *client, uci string) (int, map[string]any) {
	tb.t.Helper()
	status, obj, _ := c.do("POST", "/api/v1/game-rooms/"+tb.id+"/moves", map[string]string{"move": uci})
	return status, obj
}

func TestTwoStudentsPlayAGameToCheckmate(t *testing.T) {
	tb := openRoom(t).bothSeats()

	for i, uci := range foolsMate {
		mover := tb.white
		if i%2 == 1 {
			mover = tb.black
		}
		status, obj := tb.move(mover, uci)
		if status != 200 {
			t.Fatalf("move %d (%s): status %d (%v)", i+1, uci, status, obj)
		}
	}

	// The last move is mate, so the room closes itself.
	status, obj, _ := tb.admin.do("GET", "/api/v1/game-rooms/"+tb.id, nil)
	if status != 200 {
		t.Fatalf("read room: status %d", status)
	}
	room := obj["room"].(map[string]any)
	if room["status"] != "Finished" {
		t.Errorf("status = %v, want Finished", room["status"])
	}
	if room["result"] != "0-1" {
		t.Errorf("result = %v, want 0-1", room["result"])
	}
	if room["resultReason"] != "Checkmate" {
		t.Errorf("reason = %v, want Checkmate", room["resultReason"])
	}
	if moves := obj["moves"].([]any); len(moves) != 4 {
		t.Errorf("stored %d moves, want 4", len(moves))
	}
	// Both players are on the record, which is what the admin history reports.
	if room["white"] == nil || room["black"] == nil {
		t.Fatalf("a seat is missing: %v", room)
	}
}

// Two seats, and no third. Without this the "at most two people" rule is
// enforced only by the UI, which is to say not at all.
func TestAThirdPlayerIsTurnedAway(t *testing.T) {
	tb := openRoom(t).bothSeats()

	gate := &client{t: t, srv: tb.srv}
	gate.login("serene@jca.ac.th") // a teacher, who may otherwise play
	status, obj, _ := gate.do("POST", "/api/v1/game-rooms/join", map[string]string{"code": tb.code})
	if status != 409 {
		t.Fatalf("third join: status %d (%v), want 409", status, obj)
	}
}

// The seat claim is a conditional UPDATE precisely so this holds. If it were a
// read-then-write in Go, three simultaneous joins could all see an empty seat.
func TestSimultaneousJoinsCannotShareASeat(t *testing.T) {
	tb := openRoom(t)
	emails := []string{"penny@jca.ac.th", "uri@jca.ac.th", "serene@jca.ac.th"}

	var mu sync.Mutex
	seats := map[string]string{} // colour -> email
	rejected := 0

	var wg sync.WaitGroup
	start := make(chan struct{})
	for _, email := range emails {
		wg.Add(1)
		go func(email string) {
			defer wg.Done()
			c := &client{t: t, srv: tb.srv}
			c.login(email)
			<-start
			status, obj, _ := c.do("POST", "/api/v1/game-rooms/join", map[string]string{"code": tb.code})
			mu.Lock()
			defer mu.Unlock()
			if status == 200 {
				seats[obj["seat"].(string)] = email
			} else {
				rejected++
			}
		}(email)
	}
	close(start)
	wg.Wait()

	if len(seats) != 2 {
		t.Fatalf("filled %d seats (%v), want exactly White and Black", len(seats), seats)
	}
	if seats["White"] == "" || seats["Black"] == "" || seats["White"] == seats["Black"] {
		t.Fatalf("seats are not one each: %v", seats)
	}
	if rejected != 1 {
		t.Fatalf("%d joins were rejected, want 1", rejected)
	}
}

func TestReloadingKeepsYourSeat(t *testing.T) {
	tb := openRoom(t)
	_, first := tb.seat("penny@jca.ac.th")
	_, again := tb.seat("penny@jca.ac.th")
	if first != again {
		t.Fatalf("seat changed on rejoin: %s then %s", first, again)
	}
	// And the second claim must not have consumed the other seat.
	status, obj, _ := tb.admin.do("GET", "/api/v1/game-rooms/"+tb.id, nil)
	if status != 200 {
		t.Fatal(status)
	}
	if room := obj["room"].(map[string]any); room["black"] != nil {
		t.Fatalf("rejoining took the second seat too: %v", room)
	}
}

func TestOnlyStaffCanOpenARoom(t *testing.T) {
	srv := newServer(t)
	for _, email := range []string{"penny@jca.ac.th", "serene@jca.ac.th", "sandy01234@gmail.com"} {
		c := &client{t: t, srv: srv}
		c.login(email)
		status, _, _ := c.do("POST", "/api/v1/game-rooms", map[string]string{"label": "mine"})
		if status != 403 {
			t.Errorf("%s created a room: status %d, want 403", email, status)
		}
	}
}

func TestParentsCannotTakeASeat(t *testing.T) {
	tb := openRoom(t)
	c := &client{t: t, srv: tb.srv}
	c.login("sandy01234@gmail.com")
	status, _, _ := c.do("POST", "/api/v1/game-rooms/join", map[string]string{"code": tb.code})
	if status != 403 {
		t.Fatalf("parent join: status %d, want 403", status)
	}
}

func TestYouCannotMoveOutOfTurn(t *testing.T) {
	tb := openRoom(t).bothSeats()
	if status, obj := tb.move(tb.black, "e7e5"); status != 409 {
		t.Fatalf("black moved first: status %d (%v), want 409", status, obj)
	}
	if status, _ := tb.move(tb.white, "e2e4"); status != 200 {
		t.Fatalf("white's opening move was refused: %d", status)
	}
	// And white may not follow it with a second move.
	if status, obj := tb.move(tb.white, "d2d4"); status != 409 {
		t.Fatalf("white moved twice: status %d (%v), want 409", status, obj)
	}
}

// The point of grading moves on the server: a client that posts a move the
// position does not allow is refused, whatever its own board thinks.
func TestIllegalMovesAreRefused(t *testing.T) {
	tb := openRoom(t).bothSeats()
	for _, uci := range []string{"e2e5", "e1e3", "a1a4", "nonsense"} {
		if status, obj := tb.move(tb.white, uci); status != 400 {
			t.Errorf("%q: status %d (%v), want 400", uci, status, obj)
		}
	}
	// The board is untouched by the attempts.
	status, obj, _ := tb.admin.do("GET", "/api/v1/game-rooms/"+tb.id, nil)
	if status != 200 {
		t.Fatal(status)
	}
	if moves := obj["moves"].([]any); len(moves) != 0 {
		t.Fatalf("a refused move was stored: %v", moves)
	}
}

func TestOnlookersCannotPlayOrSeeTheRoom(t *testing.T) {
	tb := openRoom(t).bothSeats()

	outsider := &client{t: t, srv: tb.srv}
	outsider.login("serene@jca.ac.th")

	if status, obj := tb.move(outsider, "e2e4"); status != 403 {
		t.Errorf("outsider moved: status %d (%v), want 403", status, obj)
	}
	// A room someone is not in reads as missing, so ids cannot be probed.
	if status, _, _ := outsider.do("GET", "/api/v1/game-rooms/"+tb.id, nil); status != 404 {
		t.Errorf("outsider read the room: status %d, want 404", status)
	}
	if status, _, _ := outsider.do("POST", "/api/v1/game-rooms/"+tb.id+"/resign", nil); status != 403 {
		t.Errorf("outsider resigned someone else's game: status %d, want 403", status)
	}
}

// A code is a bearer credential: holding one is how you get a seat, so it must
// not appear in a list belonging to somebody who is not in the room.
func TestListingDoesNotLeakOtherPlayersRoomsOrCodes(t *testing.T) {
	tb := openRoom(t).bothSeats()

	outsider := &client{t: t, srv: tb.srv}
	outsider.login("serene@jca.ac.th")
	status, _, list := outsider.do("GET", "/api/v1/game-rooms", nil)
	if status != 200 {
		t.Fatalf("list: status %d", status)
	}
	if len(list) != 0 {
		t.Fatalf("outsider sees %d rooms, want none: %v", len(list), list)
	}

	// A player sees their own room, code included — they may pass it on.
	status, _, mine := tb.white.do("GET", "/api/v1/game-rooms", nil)
	if status != 200 || len(mine) != 1 {
		t.Fatalf("player list: status %d, %d rooms", status, len(mine))
	}
	if mine[0]["code"] != tb.code {
		t.Errorf("player cannot see their own room code: %v", mine[0]["code"])
	}
}

func TestResigningHandsTheWinToTheOpponent(t *testing.T) {
	tb := openRoom(t).bothSeats()
	if status, _ := tb.move(tb.white, "e2e4"); status != 200 {
		t.Fatal(status)
	}
	status, obj, _ := tb.black.do("POST", "/api/v1/game-rooms/"+tb.id+"/resign", nil)
	if status != 200 {
		t.Fatalf("resign: status %d (%v)", status, obj)
	}
	if obj["result"] != "1-0" {
		t.Errorf("result = %v, want 1-0 after black resigned", obj["result"])
	}
	// The game is over, so no further move lands.
	if status, _ := tb.move(tb.white, "d2d4"); status != 409 {
		t.Errorf("moved after resignation: status %d, want 409", status)
	}
}

func TestPlayCannotStartBeforeBothSeatsAreFilled(t *testing.T) {
	tb := openRoom(t)
	penny, _ := tb.seat("penny@jca.ac.th")
	if status, obj := tb.move(penny, "e2e4"); status != 409 {
		t.Fatalf("moved in a half-empty room: status %d (%v), want 409", status, obj)
	}
}

// Two clients submitting the same turn — a double-tap, or a retry after a
// timeout — must not both append. The (room, ply) primary key is what stops it.
func TestOneTurnAcceptsOneMove(t *testing.T) {
	tb := openRoom(t).bothSeats()

	var mu sync.Mutex
	accepted := 0
	var wg sync.WaitGroup
	start := make(chan struct{})
	for _, uci := range []string{"e2e4", "d2d4", "g1f3"} {
		wg.Add(1)
		go func(uci string) {
			defer wg.Done()
			<-start
			status, _ := tb.move(tb.white, uci)
			mu.Lock()
			defer mu.Unlock()
			if status == 200 {
				accepted++
			}
		}(uci)
	}
	close(start)
	wg.Wait()

	if accepted != 1 {
		t.Fatalf("%d of 3 simultaneous first moves were accepted, want 1", accepted)
	}
	status, obj, _ := tb.admin.do("GET", "/api/v1/game-rooms/"+tb.id, nil)
	if status != 200 {
		t.Fatal(status)
	}
	if moves := obj["moves"].([]any); len(moves) != 1 {
		t.Fatalf("board holds %d moves after one turn: %v", len(moves), moves)
	}
}

func TestJoiningRejectsAnUnknownCode(t *testing.T) {
	tb := openRoom(t)
	c := &client{t: t, srv: tb.srv}
	c.login("penny@jca.ac.th")
	for _, code := range []string{"ZZZZZZ", "", "   "} {
		status, _, _ := c.do("POST", "/api/v1/game-rooms/join", map[string]string{"code": code})
		if status != 404 && status != 400 {
			t.Errorf("code %q: status %d, want 404 or 400", code, status)
		}
	}
}

func TestCancellingARoomStopsPlayWithoutErasingIt(t *testing.T) {
	tb := openRoom(t).bothSeats()
	if status, _ := tb.move(tb.white, "e2e4"); status != 200 {
		t.Fatal(status)
	}
	status, _, _ := tb.admin.do("DELETE", "/api/v1/game-rooms/"+tb.id, nil)
	if status != 200 {
		t.Fatalf("cancel: status %d", status)
	}
	if status, _ := tb.move(tb.black, "e7e5"); status != 409 {
		t.Errorf("played on in a cancelled room: status %d, want 409", status)
	}
	// The game is still on the record, moves and all.
	status, obj, _ := tb.admin.do("GET", "/api/v1/game-rooms/"+tb.id, nil)
	if status != 200 {
		t.Fatalf("cancelled room is gone: status %d", status)
	}
	room := obj["room"].(map[string]any)
	if room["status"] != "Cancelled" {
		t.Errorf("status = %v, want Cancelled", room["status"])
	}
	if moves := obj["moves"].([]any); len(moves) != 1 {
		t.Errorf("cancelling lost the moves: %v", moves)
	}
}

func TestOnlyStaffCanCancelARoom(t *testing.T) {
	tb := openRoom(t).bothSeats()
	status, _, _ := tb.white.do("DELETE", "/api/v1/game-rooms/"+tb.id, nil)
	if status != 403 {
		t.Fatalf("player cancelled the room: status %d, want 403", status)
	}
}

// The legal-move list the client draws its highlights from has to match what
// the server will actually accept, or the board lies to the player.
func TestLegalMovesMatchWhatTheServerAccepts(t *testing.T) {
	tb := openRoom(t).bothSeats()
	status, obj, _ := tb.white.do("GET", "/api/v1/game-rooms/"+tb.id, nil)
	if status != 200 {
		t.Fatal(status)
	}
	legal := obj["legalMoves"].([]any)
	if len(legal) != 20 {
		t.Fatalf("opening offers %d moves, want 20", len(legal))
	}
	if obj["seat"] != "White" {
		t.Fatalf("seat = %v, want White", obj["seat"])
	}
	for _, m := range legal {
		if _, err := fmt.Sscan(m.(string)); err != nil {
			t.Fatal(err)
		}
	}
	// Spot-check that one offered move is genuinely accepted.
	if status, o := tb.move(tb.white, legal[0].(string)); status != 200 {
		t.Fatalf("offered move %v was refused: %d (%v)", legal[0], status, o)
	}
}

func TestSigningInIsRequiredThroughout(t *testing.T) {
	tb := openRoom(t)
	anon := &client{t: t, srv: tb.srv}
	for _, call := range []struct{ method, path string }{
		{"GET", "/api/v1/game-rooms"},
		{"POST", "/api/v1/game-rooms"},
		{"POST", "/api/v1/game-rooms/join"},
		{"GET", "/api/v1/game-rooms/" + tb.id},
		{"POST", "/api/v1/game-rooms/" + tb.id + "/moves"},
		{"POST", "/api/v1/game-rooms/" + tb.id + "/resign"},
		{"DELETE", "/api/v1/game-rooms/" + tb.id},
	} {
		status, _, _ := anon.do(call.method, call.path, map[string]string{})
		if status != 401 {
			t.Errorf("%s %s: status %d, want 401", call.method, call.path, status)
		}
	}
}
