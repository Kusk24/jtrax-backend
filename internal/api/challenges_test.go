package api_test

import (
	"testing"
)

/* One pupil challenging another: finding them, inviting them, and the board
   that appears when they accept. */

// twoStudents returns clients signed in as the two seeded pupils, plus their
// student ids.
func twoStudents(t *testing.T) (penny, uri *client, pennyID, uriID string) {
	t.Helper()
	srv := newServer(t)
	penny = &client{t: t, srv: srv}
	penny.login("penny@jca.ac.th")
	uri = &client{t: t, srv: srv}
	uri.login("uri@jca.ac.th")
	return penny, uri, "stu_penny", "stu_uri"
}

func TestSearchFindsAPlayerByNameAndByExactId(t *testing.T) {
	penny, _, _, uriID := twoStudents(t)

	for _, q := range []string{"uri", "Uri", uriID} {
		status, out, _ := penny.do("GET", "/api/v1/players/search?q="+q, nil)
		if status != 200 {
			t.Fatalf("search %q: %d (%v)", q, status, out)
		}
		list, _ := out["players"].([]any)
		if len(list) == 0 {
			t.Fatalf("search %q found nobody", q)
		}
		if got := list[0].(map[string]any)["studentId"]; got != uriID {
			t.Fatalf("search %q: want %s first, got %v", q, uriID, got)
		}
	}
}

// The search names other children, so what it does *not* return matters as much
// as what it does.
func TestSearchReturnsNothingBeyondANameAndId(t *testing.T) {
	penny, _, _, _ := twoStudents(t)

	_, out, _ := penny.do("GET", "/api/v1/players/search?q=uri", nil)
	row := out["players"].([]any)[0].(map[string]any)

	allowed := map[string]bool{"studentId": true, "name": true, "canPlayRated": true}
	for k := range row {
		if !allowed[k] {
			t.Fatalf("search result carries %q, which the public of this endpoint has no business seeing: %v", k, row)
		}
	}
	for _, leak := range []string{"email", "dateOfBirth", "parentName", "fideRating", "userAccountId"} {
		if _, found := row[leak]; found {
			t.Fatalf("search leaks %q", leak)
		}
	}
}

// A single letter must not return the school roll.
func TestSearchIgnoresTooShortAQuery(t *testing.T) {
	penny, _, _, _ := twoStudents(t)
	for _, q := range []string{"", "u"} {
		_, out, _ := penny.do("GET", "/api/v1/players/search?q="+q, nil)
		if list, _ := out["players"].([]any); len(list) != 0 {
			t.Fatalf("query %q returned %d players", q, len(list))
		}
	}
}

func TestSearchNeverReturnsTheSearcher(t *testing.T) {
	penny, _, pennyID, _ := twoStudents(t)
	_, out, _ := penny.do("GET", "/api/v1/players/search?q="+pennyID, nil)
	if list, _ := out["players"].([]any); len(list) != 0 {
		t.Fatalf("a pupil can find themselves, and so could challenge themselves: %v", list)
	}
}

func TestSearchNeedsASession(t *testing.T) {
	srv := newServer(t)
	anon := &client{t: t, srv: srv}
	if status, _, _ := anon.do("GET", "/api/v1/players/search?q=uri", nil); status != 401 {
		t.Fatalf("anonymous search: want 401, got %d", status)
	}
	parent := &client{t: t, srv: srv}
	parent.login("sandy01234@gmail.com")
	if status, _, _ := parent.do("GET", "/api/v1/players/search?q=uri", nil); status != 403 {
		t.Fatalf("parent search: want 403, got %d", status)
	}
}

func TestAcceptingAChallengeSeatsBothPlayers(t *testing.T) {
	penny, uri, _, uriID := twoStudents(t)

	status, sent, _ := penny.do("POST", "/api/v1/challenges", map[string]any{"studentId": uriID})
	if status != 201 {
		t.Fatalf("challenge: %d (%v)", status, sent)
	}

	// It arrives in Uri's inbox as an incoming invitation.
	_, inbox, _ := uri.do("GET", "/api/v1/challenges", nil)
	list, _ := inbox["challenges"].([]any)
	if len(list) != 1 {
		t.Fatalf("want 1 challenge waiting, got %d", len(list))
	}
	got := list[0].(map[string]any)
	if got["direction"] != "in" || got["opponentName"] != "Penny" {
		t.Fatalf("inbox reads wrong: %v", got)
	}

	id := sent["challengeId"].(string)
	status, acc, _ := uri.do("POST", "/api/v1/challenges/"+id+"/accept", nil)
	if status != 200 {
		t.Fatalf("accept: %d (%v)", status, acc)
	}
	roomID, _ := acc["gameRoomId"].(string)
	if roomID == "" {
		t.Fatalf("accepting produced no room: %v", acc)
	}

	// The board exists, is already playable, and both pupils can open it.
	for _, c := range []*client{penny, uri} {
		st, room, _ := c.do("GET", "/api/v1/game-rooms/"+roomID, nil)
		if st != 200 {
			t.Fatalf("open room: %d (%v)", st, room)
		}
		r := room["room"].(map[string]any)
		if r["status"] != "Active" {
			t.Fatalf("want an Active board, got %v", r["status"])
		}
	}
}

// Only the person challenged may answer — otherwise anybody who learned an id
// could accept on somebody else's behalf.
func TestOnlyTheChallengedPlayerCanAccept(t *testing.T) {
	penny, _, _, uriID := twoStudents(t)
	_, sent, _ := penny.do("POST", "/api/v1/challenges", map[string]any{"studentId": uriID})
	id := sent["challengeId"].(string)

	// The challenger accepting their own invitation would seat one person twice.
	if status, _, _ := penny.do("POST", "/api/v1/challenges/"+id+"/accept", nil); status != 404 {
		t.Fatalf("challenger accepting their own: want 404, got %d", status)
	}
}

func TestAChallengeCannotBeAnsweredTwice(t *testing.T) {
	penny, uri, _, uriID := twoStudents(t)
	_, sent, _ := penny.do("POST", "/api/v1/challenges", map[string]any{"studentId": uriID})
	id := sent["challengeId"].(string)

	if status, _, _ := uri.do("POST", "/api/v1/challenges/"+id+"/decline", nil); status != 200 {
		t.Fatalf("decline should succeed")
	}
	if status, _, _ := uri.do("POST", "/api/v1/challenges/"+id+"/accept", nil); status != 404 {
		t.Fatalf("accepting a declined challenge: want 404, got %d", status)
	}
}

// Holding down the button must not fill somebody's inbox.
func TestResendingAChallengeIsRefused(t *testing.T) {
	penny, _, _, uriID := twoStudents(t)
	if status, _, _ := penny.do("POST", "/api/v1/challenges", map[string]any{"studentId": uriID}); status != 201 {
		t.Fatalf("first challenge should succeed")
	}
	if status, _, _ := penny.do("POST", "/api/v1/challenges", map[string]any{"studentId": uriID}); status != 409 {
		t.Fatalf("resend: want 409, got %d", status)
	}
}

// A decline is "not now", not "never".
func TestAfterDecliningTheSamePairCanTryAgain(t *testing.T) {
	penny, uri, _, uriID := twoStudents(t)
	_, sent, _ := penny.do("POST", "/api/v1/challenges", map[string]any{"studentId": uriID})
	uri.do("POST", "/api/v1/challenges/"+sent["challengeId"].(string)+"/decline", nil)

	if status, _, _ := penny.do("POST", "/api/v1/challenges", map[string]any{"studentId": uriID}); status != 201 {
		t.Fatalf("re-challenging after a decline: want 201, got %d", status)
	}
}

func TestAPlayerCannotChallengeThemselves(t *testing.T) {
	penny, _, pennyID, _ := twoStudents(t)
	if status, _, _ := penny.do("POST", "/api/v1/challenges", map[string]any{"studentId": pennyID}); status != 400 {
		t.Fatalf("self-challenge: want 400, got %d", status)
	}
}

// A caller's list must never contain somebody else's invitation.
func TestChallengeListIsScopedToTheCaller(t *testing.T) {
	penny, uri, _, uriID := twoStudents(t)
	penny.do("POST", "/api/v1/challenges", map[string]any{"studentId": uriID})

	teacher := &client{t: t, srv: penny.srv}
	teacher.login("serene@jca.ac.th")
	_, out, _ := teacher.do("GET", "/api/v1/challenges", nil)
	if list, _ := out["challenges"].([]any); len(list) != 0 {
		t.Fatalf("an uninvolved account sees %d challenges", len(list))
	}
	_ = uri
}
