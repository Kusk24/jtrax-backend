// Throwing a game away.
//
// Cancelling ends a game and keeps the record of it. Deleting is for the rooms
// that were never records: a code minted for a lesson that did not happen, a
// board opened twice by mistake, the test rooms from an afternoon somebody
// spent learning the screen. Those piled up at the top of the list with no way
// to be rid of one.
package api_test

import "testing"

func TestStaffCanDeleteARoomThatWasNeverPlayed(t *testing.T) {
	tb := openRoom(t)

	status, body, _ := tb.admin.do("DELETE", "/api/v1/game-rooms/"+tb.id+"/record", nil)
	if status != 200 {
		t.Fatalf("delete: want 200, got %d (%v)", status, body)
	}

	status, _, _ = tb.admin.do("GET", "/api/v1/game-rooms/"+tb.id, nil)
	if status != 404 {
		t.Fatalf("room survived the delete: GET returned %d", status)
	}
}

func TestDeletingAFinishedGameTakesItsMovesWithIt(t *testing.T) {
	tb := openRoom(t).bothSeats()
	for i, uci := range foolsMate {
		mover := tb.white
		if i%2 == 1 {
			mover = tb.black
		}
		if status, obj := tb.move(mover, uci); status != 200 {
			t.Fatalf("move %s: %d (%v)", uci, status, obj)
		}
	}

	status, body, _ := tb.admin.do("DELETE", "/api/v1/game-rooms/"+tb.id+"/record", nil)
	if status != 200 {
		t.Fatalf("delete a finished game: want 200, got %d (%v)", status, body)
	}

	// game_move.game_room_id is NOT NULL, so leaving the moves behind is not
	// an option the schema allows — but a delete that half-worked would leave
	// rows pointing at a room that is gone.
	status, _, rows := tb.admin.do("GET", "/api/v1/game-rooms", nil)
	if status != 200 {
		t.Fatalf("list: %d", status)
	}
	for _, room := range rows {
		if room["gameRoomId"] == tb.id {
			t.Fatalf("deleted room is still in the list: %v", room)
		}
	}
}

// The two players are mid-move. The fix for a game that should not be running
// is to stop it, which is the other endpoint and undoes far less.
func TestAGameBeingPlayedCannotBeDeleted(t *testing.T) {
	tb := openRoom(t).bothSeats()

	status, body, _ := tb.admin.do("DELETE", "/api/v1/game-rooms/"+tb.id+"/record", nil)
	if status != 409 {
		t.Fatalf("delete an active game: want 409, got %d (%v)", status, body)
	}

	status, _, _ = tb.admin.do("GET", "/api/v1/game-rooms/"+tb.id, nil)
	if status != 200 {
		t.Fatalf("the refused delete removed the room anyway: GET %d", status)
	}

	// Stopping it first is the way through.
	if status, obj, _ := tb.admin.do("DELETE", "/api/v1/game-rooms/"+tb.id, nil); status != 200 {
		t.Fatalf("cancel: %d (%v)", status, obj)
	}
	if status, obj, _ := tb.admin.do("DELETE", "/api/v1/game-rooms/"+tb.id+"/record", nil); status != 200 {
		t.Fatalf("delete after cancelling: %d (%v)", status, obj)
	}
}

// Cancel and delete stayed separate endpoints so that an older console — which
// sends DELETE /{id} for "Stop this game" — cannot destroy a board during a
// deploy window.
func TestCancelStillOnlyCancels(t *testing.T) {
	tb := openRoom(t).bothSeats()

	if status, obj, _ := tb.admin.do("DELETE", "/api/v1/game-rooms/"+tb.id, nil); status != 200 {
		t.Fatalf("cancel: %d (%v)", status, obj)
	}

	status, room, _ := tb.admin.do("GET", "/api/v1/game-rooms/"+tb.id, nil)
	if status != 200 {
		t.Fatalf("cancel removed the room: GET %d", status)
	}
	if got := room["room"].(map[string]any)["status"]; got != "Cancelled" {
		t.Fatalf("status after cancel: %v, want Cancelled", got)
	}
}

func TestOnlyStaffCanDeleteAGame(t *testing.T) {
	tb := openRoom(t)
	penny := &client{t: t, srv: tb.srv}
	penny.login("penny@jca.ac.th")

	status, _, _ := penny.do("DELETE", "/api/v1/game-rooms/"+tb.id+"/record", nil)
	if status != 403 {
		t.Fatalf("student delete: want 403, got %d", status)
	}

	status, _, _ = tb.admin.do("GET", "/api/v1/game-rooms/"+tb.id, nil)
	if status != 200 {
		t.Fatalf("the refused delete removed the room anyway: GET %d", status)
	}
}

func TestDeletingARoomThatIsNotThere(t *testing.T) {
	tb := openRoom(t)

	status, _, _ := tb.admin.do("DELETE", "/api/v1/game-rooms/gr_nope/record", nil)
	if status != 404 {
		t.Fatalf("want 404, got %d", status)
	}
}

// A challenge is its own record — who asked whom, and when. The board it
// produced going away does not mean the invitation never happened, and
// game_challenge.game_room_id is nullable precisely so it can outlive one.
func TestDeletingARoomUnhooksItsChallengeRatherThanDestroyingIt(t *testing.T) {
	penny, uri, _, uriID := twoStudents(t)
	srv := penny.srv
	admin := &client{t: t, srv: srv}
	admin.login("admin@jca.ac.th")

	_, sent, _ := penny.do("POST", "/api/v1/challenges", map[string]any{"studentId": uriID})
	id, _ := sent["challengeId"].(string)
	status, acc, _ := uri.do("POST", "/api/v1/challenges/"+id+"/accept", nil)
	if status != 200 {
		t.Fatalf("accept: %d (%v)", status, acc)
	}
	roomID := acc["gameRoomId"].(string)

	// Accepting produces an Active board, so it has to be stopped first.
	if status, obj, _ := admin.do("DELETE", "/api/v1/game-rooms/"+roomID, nil); status != 200 {
		t.Fatalf("cancel: %d (%v)", status, obj)
	}
	status, body, _ := admin.do("DELETE", "/api/v1/game-rooms/"+roomID+"/record", nil)
	if status != 200 {
		t.Fatalf("delete: %d (%v)", status, body)
	}

	// The room is gone and the challenge is not — if the delete had been
	// refused by the foreign key, the room would still be here.
	if status, _, _ := admin.do("GET", "/api/v1/game-rooms/"+roomID, nil); status != 404 {
		t.Fatalf("room survived: GET %d", status)
	}
	if status, _, _ := penny.do("GET", "/api/v1/challenges", nil); status != 200 {
		t.Fatalf("challenges no longer readable: %d", status)
	}
}
