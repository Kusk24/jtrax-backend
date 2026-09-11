package api_test

import "testing"

func recordSolo(t *testing.T, c *client, body map[string]any) int {
	t.Helper()
	status, _, _ := c.do("POST", "/api/v1/games/solo", body)
	return status
}

func historyOf(t *testing.T, c *client, studentID string) []map[string]any {
	t.Helper()
	status, obj, _ := c.do("GET", "/api/v1/students/"+studentID+"/history", nil)
	if status != 200 {
		t.Fatalf("history: status %d (%v)", status, obj)
	}
	raw, _ := obj["history"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, row := range raw {
		if m, ok := row.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// A game against the computer used to vanish the moment the tab closed.
func TestAGameAgainstTheComputerIsKept(t *testing.T) {
	srv := newServer(t)
	penny := &client{t: t, srv: srv}
	penny.login("penny@jca.ac.th")

	if status := recordSolo(t, penny, map[string]any{
		"opponent": "novice", "moves": []string{"e2e4", "e7e5", "g1f3"},
		"result": "1-0", "reason": "Checkmate",
	}); status != 201 {
		t.Fatalf("recording a solo game: status %d", status)
	}

	hist := historyOf(t, penny, "stu_penny")
	var solo map[string]any
	for _, e := range hist {
		if e["kind"] == "solo" {
			solo = e
		}
	}
	if solo == nil {
		t.Fatalf("the game is not in the history: %v", hist)
	}
	if solo["against"] != "novice" || solo["result"] != "1-0" {
		t.Errorf("got %v, want novice / 1-0", solo)
	}
	if n, _ := solo["moves"].(float64); n != 3 {
		t.Errorf("moves = %v, want 3", solo["moves"])
	}
}

// The body arrives from a browser, so it is checked at the boundary.
func TestASoloGameIsValidatedAtTheBoundary(t *testing.T) {
	srv := newServer(t)
	penny := &client{t: t, srv: srv}
	penny.login("penny@jca.ac.th")

	for _, tc := range []struct {
		name string
		body map[string]any
		want int
	}{
		{"unknown opponent", map[string]any{"opponent": "stockfish16", "moves": []string{"e2e4"}}, 422},
		{"no moves at all", map[string]any{"opponent": "novice", "moves": []string{}}, 422},
		{"an invented result", map[string]any{"opponent": "novice", "moves": []string{"e2e4"}, "result": "2-0"}, 422},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if status := recordSolo(t, penny, tc.body); status != tc.want {
				t.Fatalf("status %d, want %d", status, tc.want)
			}
		})
	}
}

// Puzzles were always recorded and never shown anywhere.
func TestPuzzlesAppearInTheHistory(t *testing.T) {
	srv := newServer(t)
	penny := &client{t: t, srv: srv}
	penny.login("penny@jca.ac.th")
	dailySet(t, penny) // assigning is what creates the attempts

	var puzzles int
	for _, e := range historyOf(t, penny, "stu_penny") {
		if e["kind"] == "puzzle" {
			puzzles++
		}
	}
	if puzzles != 3 {
		t.Fatalf("got %d puzzles in the history, want 3", puzzles)
	}
}

// Who may read a pupil's chess. A parent of one child must not be able to read
// another's by changing the id in the URL.
func TestHistoryIsScopedToPeopleWhoMaySeeThePupil(t *testing.T) {
	srv := newServer(t)

	// Their own.
	penny := &client{t: t, srv: srv}
	penny.login("penny@jca.ac.th")
	if status, _, _ := penny.do("GET", "/api/v1/students/stu_penny/history", nil); status != 200 {
		t.Errorf("a pupil reading their own history: %d", status)
	}
	// Somebody else's.
	if status, _, _ := penny.do("GET", "/api/v1/students/stu_uri/history", nil); status != 404 {
		t.Errorf("a pupil reading a classmate's history: want 404, got %d", status)
	}
	// Their parent, who has both children.
	sandy := &client{t: t, srv: srv}
	sandy.login("sandy01234@gmail.com")
	if status, _, _ := sandy.do("GET", "/api/v1/students/stu_penny/history", nil); status != 200 {
		t.Errorf("a parent reading their child's history: %d", status)
	}
	// Staff see the academy.
	admin := &client{t: t, srv: srv}
	admin.login("admin@jca.ac.th")
	if status, _, _ := admin.do("GET", "/api/v1/students/stu_penny/history", nil); status != 200 {
		t.Errorf("staff reading a pupil's history: %d", status)
	}
	// A pupil who does not exist is the same answer as one you may not see, so
	// the URL cannot be used to discover ids.
	if status, _, _ := admin.do("GET", "/api/v1/students/stu_nobody/history", nil); status != 404 {
		t.Errorf("unknown pupil: want 404, got %d", status)
	}
}

// Only a pupil records their own practice games.
func TestOnlyAStudentRecordsASoloGame(t *testing.T) {
	srv := newServer(t)
	for _, email := range []string{"sandy01234@gmail.com", "admin@jca.ac.th"} {
		c := &client{t: t, srv: srv}
		c.login(email)
		if status := recordSolo(t, c, map[string]any{
			"opponent": "novice", "moves": []string{"e2e4"},
		}); status != 403 {
			t.Errorf("%s recording a solo game: want 403, got %d", email, status)
		}
	}
}
