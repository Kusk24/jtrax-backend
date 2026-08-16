package api_test

import (
	"net/http"
	"strings"
	"testing"
)

/* A small event set up through the API, the way an arbiter would. */

type event struct {
	c       *client
	id      string
	players map[string]string // name -> registration id
}

func newEvent(t *testing.T, names ...string) *event {
	t.Helper()
	c := &client{t: t, srv: newServer(t)}
	c.login("admin@jca.ac.th")

	status, tour, _ := c.do("POST", "/api/v1/tournaments", map[string]any{
		"name": "JCA Autumn Rapid", "tournament_status": "Ongoing", "start_date": "2026-09-05",
	})
	if status != 201 {
		t.Fatalf("create tournament: %d (%v)", status, tour)
	}
	e := &event{c: c, id: tour["tournament_id"].(string), players: map[string]string{}}

	// The seed has two students; more players need accounts to hang off.
	seeds := []string{"stu_penny", "stu_uri"}
	for i, name := range names {
		studentID := ""
		if i < len(seeds) {
			studentID = seeds[i]
		} else {
			st, s, _ := c.do("POST", "/api/v1/students", map[string]any{"name": name})
			if st != 201 {
				t.Fatalf("create student %s: %d (%v)", name, st, s)
			}
			studentID = s["student_id"].(string)
		}
		st, reg, _ := c.do("POST", "/api/v1/tournament-registrations", map[string]any{
			"tournament_id": e.id, "student_id": studentID, "participant_name": name,
		})
		if st != 201 {
			t.Fatalf("register %s: %d (%v)", name, st, reg)
		}
		e.players[name] = reg["tournament_registration_id"].(string)
	}
	return e
}

// round creates a round and sets its boards. Each board is {white, black} by
// name; an empty black is a bye.
func (e *event) round(t *testing.T, boards ...[2]string) string {
	t.Helper()
	status, rd, _ := e.c.do("POST", "/api/v1/tournaments/"+e.id+"/rounds", nil)
	if status != 201 {
		t.Fatalf("create round: %d (%v)", status, rd)
	}
	roundID := rd["roundId"].(string)
	pairings := []map[string]any{}
	for i, b := range boards {
		p := map[string]any{"board": i + 1, "whiteRegistrationId": e.players[b[0]]}
		if b[1] == "" {
			p["result"] = "bye"
		} else {
			p["blackRegistrationId"] = e.players[b[1]]
		}
		pairings = append(pairings, p)
	}
	status, out, _ := e.c.do("PUT", "/api/v1/tournaments/rounds/"+roundID+"/pairings",
		map[string]any{"pairings": pairings})
	if status != 200 {
		t.Fatalf("set pairings: %d (%v)", status, out)
	}
	return roundID
}

// record finds the board with the given white player and sets its result.
func (e *event) record(t *testing.T, roundID, white, result string) int {
	t.Helper()
	_, obj, _ := e.c.do("GET", "/api/v1/tournaments/"+e.id+"/results", nil)
	for _, r := range obj["rounds"].([]any) {
		rd := r.(map[string]any)
		if rd["roundId"] != roundID {
			continue
		}
		for _, p := range rd["pairings"].([]any) {
			pr := p.(map[string]any)
			if pr["white"] == white {
				status, _, _ := e.c.do("PATCH",
					"/api/v1/tournaments/pairings/"+pr["pairingId"].(string),
					map[string]string{"result": result})
				return status
			}
		}
	}
	t.Fatalf("no board with %s as white in round %s", white, roundID)
	return 0
}

func (e *event) standings(t *testing.T) []map[string]any {
	t.Helper()
	_, obj, _ := e.c.do("GET", "/api/v1/tournaments/"+e.id+"/results", nil)
	out := []map[string]any{}
	for _, s := range obj["standings"].([]any) {
		out = append(out, s.(map[string]any))
	}
	return out
}

/* ---- the arbiter's flow ---- */

func TestTournamentResultsFlow(t *testing.T) {
	e := newEvent(t, "Penny", "Uri", "Ana", "Bo")

	r1 := e.round(t, [2]string{"Penny", "Uri"}, [2]string{"Ana", "Bo"})
	if s := e.record(t, r1, "Penny", "1-0"); s != 200 {
		t.Fatalf("record: %d", s)
	}
	e.record(t, r1, "Ana", "1/2-1/2")

	table := e.standings(t)
	if table[0]["name"] != "Penny" || table[0]["points"].(float64) != 1 {
		t.Errorf("leader = %v", table[0])
	}
	if table[0]["rank"].(float64) != 1 {
		t.Errorf("leader rank = %v", table[0]["rank"])
	}
	// Uri lost and must still be in their own tournament.
	found := false
	for _, row := range table {
		if row["name"] == "Uri" {
			found = true
			if row["points"].(float64) != 0 || row["losses"].(float64) != 1 {
				t.Errorf("Uri = %v", row)
			}
		}
	}
	if !found {
		t.Error("Uri missing from the table")
	}
}

func TestRoundCompletesWhenEveryBoardIsIn(t *testing.T) {
	e := newEvent(t, "Penny", "Uri")
	r1 := e.round(t, [2]string{"Penny", "Uri"})

	_, obj, _ := e.c.do("GET", "/api/v1/tournaments/"+e.id+"/results", nil)
	rd := obj["rounds"].([]any)[0].(map[string]any)
	if rd["status"] != "Playing" {
		t.Errorf("a paired round should be Playing, got %v", rd["status"])
	}

	e.record(t, r1, "Penny", "1-0")
	_, obj, _ = e.c.do("GET", "/api/v1/tournaments/"+e.id+"/results", nil)
	rd = obj["rounds"].([]any)[0].(map[string]any)
	// Derived from the boards, so it cannot drift from what it describes.
	if rd["status"] != "Completed" {
		t.Errorf("every board is in, so the round should be Completed, got %v", rd["status"])
	}
}

func TestByeIsARowNotAGap(t *testing.T) {
	e := newEvent(t, "Penny", "Uri", "Ana")
	e.round(t, [2]string{"Penny", "Uri"}, [2]string{"Ana", ""})

	for _, row := range e.standings(t) {
		if row["name"] == "Ana" {
			if row["points"].(float64) != 1 {
				t.Errorf("a bye is a full point: %v", row)
			}
			if row["played"].(float64) != 0 {
				t.Errorf("a bye is not a game played: %v", row)
			}
		}
	}
}

/* ---- what the endpoints refuse ---- */

func TestPairingsRejectAPlayerOnTwoBoards(t *testing.T) {
	e := newEvent(t, "Penny", "Uri", "Ana")
	status, rd, _ := e.c.do("POST", "/api/v1/tournaments/"+e.id+"/rounds", nil)
	if status != 201 {
		t.Fatal(rd)
	}
	roundID := rd["roundId"].(string)

	status, obj, _ := e.c.do("PUT", "/api/v1/tournaments/rounds/"+roundID+"/pairings", map[string]any{
		"pairings": []map[string]any{
			{"board": 1, "whiteRegistrationId": e.players["Penny"], "blackRegistrationId": e.players["Uri"]},
			{"board": 2, "whiteRegistrationId": e.players["Penny"], "blackRegistrationId": e.players["Ana"]},
		},
	})
	if status != 400 {
		t.Fatalf("Penny is on two boards at once: status %d (%v)", status, obj)
	}
}

func TestPairingsRejectSelfAndOutsiders(t *testing.T) {
	e := newEvent(t, "Penny", "Uri")
	_, rd, _ := e.c.do("POST", "/api/v1/tournaments/"+e.id+"/rounds", nil)
	roundID := rd["roundId"].(string)
	url := "/api/v1/tournaments/rounds/" + roundID + "/pairings"

	if s, _, _ := e.c.do("PUT", url, map[string]any{"pairings": []map[string]any{
		{"board": 1, "whiteRegistrationId": e.players["Penny"], "blackRegistrationId": e.players["Penny"]},
	}}); s != 400 {
		t.Errorf("a player paired against themselves: %d", s)
	}
	if s, _, _ := e.c.do("PUT", url, map[string]any{"pairings": []map[string]any{
		{"board": 1, "whiteRegistrationId": e.players["Penny"], "blackRegistrationId": "treg_not_in_this_event"},
	}}); s != 400 {
		t.Errorf("someone not registered was paired: %d", s)
	}
}

func TestUnknownResultIsRejected(t *testing.T) {
	e := newEvent(t, "Penny", "Uri")
	r1 := e.round(t, [2]string{"Penny", "Uri"})
	if s := e.record(t, r1, "Penny", "Penny won I think"); s != 400 {
		t.Errorf("a made-up result was accepted: %d", s)
	}
}

func TestOnlyStaffRecordResults(t *testing.T) {
	e := newEvent(t, "Penny", "Uri")
	e.round(t, [2]string{"Penny", "Uri"})
	_, obj, _ := e.c.do("GET", "/api/v1/tournaments/"+e.id+"/results", nil)
	rd := obj["rounds"].([]any)[0].(map[string]any)
	pairingID := rd["pairings"].([]any)[0].(map[string]any)["pairingId"].(string)

	for _, who := range []string{"penny@jca.ac.th", "sandy01234@gmail.com", "serene@jca.ac.th"} {
		other := &client{t: t, srv: e.c.srv}
		other.login(who)
		if s, _, _ := other.do("PATCH", "/api/v1/tournaments/pairings/"+pairingID,
			map[string]string{"result": "0-1"}); s != 403 {
			t.Errorf("%s recorded a result: status %d, want 403", who, s)
		}
		if s, _, _ := other.do("POST", "/api/v1/tournaments/"+e.id+"/rounds", nil); s != 403 {
			t.Errorf("%s created a round: status %d, want 403", who, s)
		}
	}
}

/* ---- the public page ---- */

func TestPublicResultsAreOffUntilPublished(t *testing.T) {
	e := newEvent(t, "Penny", "Uri")
	r1 := e.round(t, [2]string{"Penny", "Uri"})
	e.record(t, r1, "Penny", "1-0")

	anon := &client{t: t, srv: e.c.srv} // no token
	if s, _ := anon.raw("GET", "/api/v1/public/tournaments/"+e.id+"/results"); s != 404 {
		t.Fatalf("unpublished results were readable: status %d", s)
	}
	// A tournament that does not exist answers identically, so the endpoint
	// cannot be used to discover which ids are real.
	if s, _ := anon.raw("GET", "/api/v1/public/tournaments/trn_nonexistent/results"); s != 404 {
		t.Errorf("want the same 404 for a missing tournament, got %d", s)
	}

	if s, _, _ := e.c.do("PATCH", "/api/v1/tournaments/"+e.id,
		map[string]any{"results_public": true}); s != 200 {
		t.Fatalf("publish failed: %d", s)
	}
	status, body := anon.raw("GET", "/api/v1/public/tournaments/"+e.id+"/results")
	if status != 200 {
		t.Fatalf("published results not readable: %d", status)
	}
	if !strings.Contains(body, "Penny") {
		t.Errorf("standings missing from the public payload: %s", body)
	}
}

// The public payload must carry only what is already pinned to the wall at a
// tournament hall. Checked by value, not by field name: a change that leaked an
// internal id under a different key would pass a name-based test.
func TestPublicResultsLeakNoInternalIds(t *testing.T) {
	e := newEvent(t, "Penny", "Uri")
	r1 := e.round(t, [2]string{"Penny", "Uri"})
	e.record(t, r1, "Penny", "1-0")
	e.c.do("PATCH", "/api/v1/tournaments/"+e.id, map[string]any{"results_public": true})

	anon := &client{t: t, srv: e.c.srv}
	_, body := anon.raw("GET", "/api/v1/public/tournaments/"+e.id+"/results")

	for _, secret := range []string{e.players["Penny"], e.players["Uri"], "stu_penny", "stu_uri"} {
		if strings.Contains(body, secret) {
			t.Errorf("internal id %q appears in the public payload: %s", secret, body)
		}
	}
}

func TestPublicResultsNeedNoSession(t *testing.T) {
	e := newEvent(t, "Penny", "Uri")
	e.c.do("PATCH", "/api/v1/tournaments/"+e.id, map[string]any{"results_public": true})

	// Straight at the server with no Authorization header at all.
	res, err := http.Get(e.c.srv.URL + "/api/v1/public/tournaments/" + e.id + "/results")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Errorf("an anonymous reader got %d", res.StatusCode)
	}
}

/* ---- proposals ---- */

func TestProposedPairingsAvoidARematch(t *testing.T) {
	e := newEvent(t, "Penny", "Uri", "Ana", "Bo")
	r1 := e.round(t, [2]string{"Penny", "Uri"}, [2]string{"Ana", "Bo"})
	e.record(t, r1, "Penny", "1-0")
	e.record(t, r1, "Ana", "1-0")

	status, obj, _ := e.c.do("GET", "/api/v1/tournaments/"+e.id+"/proposed-pairings", nil)
	if status != 200 {
		t.Fatalf("propose: %d (%v)", status, obj)
	}
	pairs := obj["pairings"].([]any)
	if len(pairs) != 2 {
		t.Fatalf("want 2 boards, got %d", len(pairs))
	}
	for _, p := range pairs {
		pr := p.(map[string]any)
		w, b := pr["white"], pr["black"]
		if (w == "Penny" && b == "Uri") || (w == "Uri" && b == "Penny") ||
			(w == "Ana" && b == "Bo") || (w == "Bo" && b == "Ana") {
			t.Errorf("proposed a round-1 rematch: %v vs %v", w, b)
		}
	}
	// The two winners should meet on board one.
	top := pairs[0].(map[string]any)
	if !((top["white"] == "Penny" && top["black"] == "Ana") ||
		(top["white"] == "Ana" && top["black"] == "Penny")) {
		t.Errorf("top board = %v vs %v, want the two winners", top["white"], top["black"])
	}
}
