package api_test

import (
	"testing"
)

/* The console watching a class of boards: games it seats itself, a time
   control on any game, draw offers, and the moves on the list read. */

// assignedGame has the admin open a game with Penny as White and Uri as Black,
// and both of them enter it, so it is in play.
func assignedGame(t *testing.T, extra map[string]any) (admin, penny, uri *client, room map[string]any) {
	t.Helper()
	admin, penny, uri, room = seatedGame(t, extra)
	base := "/api/v1/game-rooms/" + room["gameRoomId"].(string)
	for _, c := range []*client{penny, uri} {
		if status, obj, _ := c.do("POST", base+"/enter", nil); status != 200 {
			t.Fatalf("enter: %d (%v)", status, obj)
		}
	}
	return admin, penny, uri, room
}

// seatedGame has the admin open a game with Penny as White and Uri as Black,
// which neither has entered yet.
func seatedGame(t *testing.T, extra map[string]any) (admin, penny, uri *client, room map[string]any) {
	t.Helper()
	penny, uri, pennyID, uriID := twoStudents(t)
	admin = &client{t: t, srv: penny.srv}
	admin.login("admin@jca.ac.th")
	body := map[string]any{"whiteStudentId": pennyID, "blackStudentId": uriID}
	for k, v := range extra {
		body[k] = v
	}
	status, room, _ := admin.do("POST", "/api/v1/game-rooms", body)
	if status != 201 {
		t.Fatalf("create assigned game: %d (%v)", status, room)
	}
	return admin, penny, uri, room
}

func TestTheConsoleCanSeatBothPlayers(t *testing.T) {
	_, penny, uri, room := seatedGame(t, nil)

	if room["status"] != "Open" {
		t.Fatalf("status = %v, want the game waiting until both students enter", room["status"])
	}
	if w, _ := room["white"].(map[string]any); w == nil || w["studentId"] != "stu_penny" {
		t.Fatalf("white = %v, want Penny", room["white"])
	}
	if b, _ := room["black"].(map[string]any); b == nil || b["studentId"] != "stu_uri" {
		t.Fatalf("black = %v, want Uri", room["black"])
	}

	// Both pupils find it in their own list without typing a code.
	id := room["gameRoomId"].(string)
	for name, c := range map[string]*client{"penny": penny, "uri": uri} {
		_, _, list := c.do("GET", "/api/v1/game-rooms", nil)
		found := false
		for _, r := range list {
			found = found || r["gameRoomId"] == id
		}
		if !found {
			t.Fatalf("%s's game list does not have the game they were seated in", name)
		}
	}

}

// The game starts when both students have pressed Enter, not when the office
// set it up — until then nobody can move.
func TestAnAssignedGameStartsWhenBothEnter(t *testing.T) {
	_, penny, uri, room := seatedGame(t, nil)
	base := "/api/v1/game-rooms/" + room["gameRoomId"].(string)

	if status, _, _ := penny.do("POST", base+"/moves", map[string]string{"move": "e2e4"}); status != 409 {
		t.Fatalf("a move before anyone entered: %d, want 409", status)
	}

	_, got, _ := penny.do("POST", base+"/enter", nil)
	r := got["room"].(map[string]any)
	if r["status"] != "Open" || r["whiteEntered"] != true || r["blackEntered"] != false {
		t.Fatalf("after White enters: status %v, white %v, black %v — want waiting on Black",
			r["status"], r["whiteEntered"], r["blackEntered"])
	}
	// Pressing it twice changes nothing.
	if status, _, _ := penny.do("POST", base+"/enter", nil); status != 200 {
		t.Fatalf("entering twice: %d", status)
	}

	_, got, _ = uri.do("POST", base+"/enter", nil)
	if r := got["room"].(map[string]any); r["status"] != "Active" || r["startedAt"] == "" {
		t.Fatalf("after both enter: status %v, startedAt %v — want in play", r["status"], r["startedAt"])
	}
	if status, obj, _ := penny.do("POST", base+"/moves", map[string]string{"move": "e2e4"}); status != 200 {
		t.Fatalf("first move: %d (%v)", status, obj)
	}
}

// Typing the code counts as entering, and nobody else can take a seat the
// office assigned.
func TestTheCodeEntersAnAssignedSeatAndKeepsOthersOut(t *testing.T) {
	_, penny, _, room := seatedGame(t, nil)
	code := room["code"].(string)

	if status, obj, _ := penny.do("POST", "/api/v1/game-rooms/join", map[string]string{"code": code}); status != 200 || obj["seat"] != "White" {
		t.Fatalf("Penny joining her own game by code: %d (%v)", status, obj)
	}
	_, got, _ := penny.do("GET", "/api/v1/game-rooms/"+room["gameRoomId"].(string), nil)
	if got["room"].(map[string]any)["whiteEntered"] != true {
		t.Fatal("joining by code did not count as entering")
	}

	stranger := &client{t: t, srv: penny.srv}
	stranger.login("serene@jca.ac.th")
	if status, _, _ := stranger.do("POST", "/api/v1/game-rooms/join", map[string]string{"code": code}); status != 409 {
		t.Fatalf("someone else taking an assigned seat: %d, want 409", status)
	}
	if status, _, _ := stranger.do("POST", "/api/v1/game-rooms/"+room["gameRoomId"].(string)+"/enter", nil); status != 404 {
		t.Fatalf("someone else entering: %d, want 404", status)
	}
}

func TestSeatingOneOrTheSamePlayerIsRefused(t *testing.T) {
	penny, _, pennyID, _ := twoStudents(t)
	admin := &client{t: t, srv: penny.srv}
	admin.login("admin@jca.ac.th")

	for name, body := range map[string]map[string]any{
		"only white":     {"whiteStudentId": pennyID},
		"the same twice": {"whiteStudentId": pennyID, "blackStudentId": pennyID},
		"nobody real":    {"whiteStudentId": pennyID, "blackStudentId": "stu_nobody"},
	} {
		if status, obj, _ := admin.do("POST", "/api/v1/game-rooms", body); status != 400 {
			t.Errorf("%s: status %d (%v), want 400", name, status, obj)
		}
	}
}

func TestATimeControlIsKeptOnlyWhenOneWasChosen(t *testing.T) {
	_, _, _, timed := assignedGame(t, map[string]any{"timed": true, "clockLimit": 600, "clockIncrement": 5})
	tc, _ := timed["timeControl"].(map[string]any)
	if tc == nil || tc["limit"] != float64(600) || tc["increment"] != float64(5) {
		t.Fatalf("timeControl = %v, want 10+5", timed["timeControl"])
	}
	// Unrated: our board keeps no time, so there is no running clock to show.
	if timed["clock"] != nil {
		t.Fatalf("an unrated game has a clock: %v", timed["clock"])
	}

	_, _, _, untimed := assignedGame(t, nil)
	if untimed["timeControl"] != nil {
		t.Fatalf("a game with no time control chosen reads as timed: %v", untimed["timeControl"])
	}
}

func TestADrawOfferAcceptedEndsTheGameByAgreement(t *testing.T) {
	_, penny, uri, room := assignedGame(t, nil)
	base := "/api/v1/game-rooms/" + room["gameRoomId"].(string)

	if status, obj, _ := penny.do("POST", base+"/draw/offer", nil); status != 200 {
		t.Fatalf("offer: %d (%v)", status, obj)
	}
	_, got, _ := uri.do("GET", base, nil)
	if got["room"].(map[string]any)["drawOffer"] != "White" {
		t.Fatalf("drawOffer = %v, want White's offer standing", got["room"].(map[string]any)["drawOffer"])
	}
	// Nobody accepts their own offer.
	if status, _, _ := penny.do("POST", base+"/draw/accept", nil); status != 409 {
		t.Fatalf("offerer accepting: %d, want 409", status)
	}
	if status, obj, _ := uri.do("POST", base+"/draw/accept", nil); status != 200 {
		t.Fatalf("accept: %d (%v)", status, obj)
	}
	_, got, _ = penny.do("GET", base, nil)
	r := got["room"].(map[string]any)
	if r["status"] != "Finished" || r["result"] != "1/2-1/2" || r["resultReason"] != "Agreement" {
		t.Fatalf("after accepting: status %v, result %v, reason %v — want a draw by agreement",
			r["status"], r["result"], r["resultReason"])
	}
}

func TestADrawOfferIsClearedByDecliningOrByAMove(t *testing.T) {
	_, penny, uri, room := assignedGame(t, nil)
	base := "/api/v1/game-rooms/" + room["gameRoomId"].(string)
	offerOf := func() any {
		_, got, _ := penny.do("GET", base, nil)
		return got["room"].(map[string]any)["drawOffer"]
	}

	penny.do("POST", base+"/draw/offer", nil)
	if status, _, _ := uri.do("POST", base+"/draw/decline", nil); status != 200 {
		t.Fatalf("decline: %d", status)
	}
	if o := offerOf(); o != nil {
		t.Fatalf("declined offer still standing: %v", o)
	}

	// Offering and then playing on withdraws it.
	penny.do("POST", base+"/draw/offer", nil)
	penny.do("POST", base+"/moves", map[string]string{"move": "e2e4"})
	if o := offerOf(); o != nil {
		t.Fatalf("offer survived a move: %v", o)
	}
	if status, _, _ := uri.do("POST", base+"/draw/accept", nil); status != 409 {
		t.Fatalf("accepting a withdrawn offer: %d, want 409", status)
	}
}

func TestTheConsoleListCarriesEachGamesMoves(t *testing.T) {
	admin, penny, uri, room := assignedGame(t, nil)
	base := "/api/v1/game-rooms/" + room["gameRoomId"].(string)
	penny.do("POST", base+"/moves", map[string]string{"move": "e2e4"})
	uri.do("POST", base+"/moves", map[string]string{"move": "e7e5"})

	_, _, list := admin.do("GET", "/api/v1/game-rooms?moves=1", nil)
	var mine map[string]any
	for _, r := range list {
		if r["gameRoomId"] == room["gameRoomId"] {
			mine = r
		}
	}
	sans, _ := mine["sans"].([]any)
	if len(sans) != 2 || sans[0] != "e4" || sans[1] != "e5" || mine["lastUci"] != "e7e5" {
		t.Fatalf("sans = %v, lastUci = %v — want e4 e5 ending e7e5", mine["sans"], mine["lastUci"])
	}
	if mine["turn"] != "White" {
		t.Fatalf("turn = %v, want White to move", mine["turn"])
	}

	// A plain read stays light.
	_, _, plain := admin.do("GET", "/api/v1/game-rooms", nil)
	if _, has := plain[0]["sans"]; has {
		t.Fatal("the list carries every move without being asked")
	}
}

// One child, one board: a student still in a game cannot be seated in another.
func TestAStudentInAGameCannotBeSeatedInAnother(t *testing.T) {
	admin, _, _, _ := assignedGame(t, nil)
	status, obj, _ := admin.do("POST", "/api/v1/game-rooms",
		map[string]any{"whiteStudentId": "stu_uri", "blackStudentId": "stu_penny"})
	if status != 409 {
		t.Fatalf("seating two students already at a board: %d (%v), want 409", status, obj)
	}
}

// The bell goes mid-game: the office pauses it, and resumes it another day;
// the two then enter again and carry on from the same position. The players
// cannot resume it themselves.
func TestAGameOnHoldResumesWhereItLeftOff(t *testing.T) {
	admin, penny, uri, room := assignedGame(t, nil)
	id := room["gameRoomId"].(string)
	base := "/api/v1/game-rooms/" + id
	penny.do("POST", base+"/moves", map[string]string{"move": "e2e4"})

	if status, _, _ := penny.do("POST", base+"/stop", nil); status != 403 {
		t.Fatalf("a student pausing the game: %d, want 403", status)
	}
	if status, obj, _ := admin.do("POST", base+"/stop", nil); status != 200 {
		t.Fatalf("stop: %d (%v)", status, obj)
	}
	_, got, _ := uri.do("GET", base, nil)
	r := got["room"].(map[string]any)
	if r["status"] != "Open" || r["stopped"] != true {
		t.Fatalf("after stopping: %v — want Open and paused", r)
	}
	if n := len(got["moves"].([]any)); n != 1 {
		t.Fatalf("moves after stopping = %d, want the one played kept", n)
	}
	if status, _, _ := uri.do("POST", base+"/moves", map[string]string{"move": "e7e5"}); status != 409 {
		t.Fatalf("a move on a paused game: %d, want 409", status)
	}
	// Pressing Enter does not bring it back; only the office does.
	if status, _, _ := uri.do("POST", base+"/enter", nil); status != 409 {
		t.Fatalf("a student entering a paused game: %d, want 409", status)
	}
	if status, _, _ := uri.do("POST", base+"/resume", nil); status != 403 {
		t.Fatalf("a student resuming: %d, want 403", status)
	}
	if status, _, _ := admin.do("POST", base+"/stop", nil); status != 409 {
		t.Fatalf("pausing a paused game: %d, want 409", status)
	}

	if status, obj, _ := admin.do("POST", base+"/resume", nil); status != 200 {
		t.Fatalf("resume: %d (%v)", status, obj)
	}
	// Resumed is not in play: the two may not be at the board yet, so it
	// waits for both to press Enter again.
	_, got, _ = uri.do("GET", base, nil)
	r = got["room"].(map[string]any)
	if r["status"] != "Open" || r["stopped"] != false || r["whiteEntered"] != false || r["blackEntered"] != false {
		t.Fatalf("after resuming: %v — want waiting for both to enter", r)
	}
	if status, _, _ := uri.do("POST", base+"/moves", map[string]string{"move": "e7e5"}); status != 409 {
		t.Fatalf("a move before both re-enter: %d, want 409", status)
	}
	penny.do("POST", base+"/enter", nil)
	uri.do("POST", base+"/enter", nil)
	_, got, _ = uri.do("GET", base, nil)
	if r := got["room"].(map[string]any); r["status"] != "Active" {
		t.Fatalf("after both enter: status %v, want in play", r["status"])
	}
	if status, obj, _ := uri.do("POST", base+"/moves", map[string]string{"move": "e7e5"}); status != 200 {
		t.Fatalf("Black's reply after resuming: %d (%v)", status, obj)
	}
	if status, _, _ := admin.do("POST", base+"/resume", nil); status != 409 {
		t.Fatalf("resuming a game in play: %d, want 409", status)
	}
}

// A stopped game can be thrown away, like a waiting one; one in play cannot.
func TestAStoppedGameCanBeRemoved(t *testing.T) {
	admin, _, _, room := assignedGame(t, nil)
	base := "/api/v1/game-rooms/" + room["gameRoomId"].(string)
	if status, _, _ := admin.do("DELETE", base+"/record", nil); status != 409 {
		t.Fatalf("removing a game in play: %d, want 409", status)
	}
	admin.do("POST", base+"/stop", nil)
	if status, obj, _ := admin.do("DELETE", base+"/record", nil); status != 200 {
		t.Fatalf("removing a stopped game: %d (%v)", status, obj)
	}
}

// Ending a game keeps it: it stops for good, with its moves, and stays in the
// record — unlike removing it. A paused game can be ended too.
func TestEndingAGameKeepsIt(t *testing.T) {
	admin, penny, _, room := assignedGame(t, nil)
	base := "/api/v1/game-rooms/" + room["gameRoomId"].(string)
	penny.do("POST", base+"/moves", map[string]string{"move": "e2e4"})
	admin.do("POST", base+"/stop", nil)

	if status, obj, _ := admin.do("DELETE", base, nil); status != 200 {
		t.Fatalf("end: %d (%v)", status, obj)
	}
	status, got, _ := penny.do("GET", base, nil)
	if status != 200 {
		t.Fatalf("an ended game is gone: %d", status)
	}
	if r := got["room"].(map[string]any); r["status"] != "Cancelled" {
		t.Fatalf("status = %v, want ended", r["status"])
	}
	if n := len(got["moves"].([]any)); n != 1 {
		t.Fatalf("moves = %d, want the one played kept", n)
	}
}
