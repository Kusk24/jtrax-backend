package api_test

import (
	"database/sql"
	"strings"
	"testing"
)

func puzzleList(t *testing.T, c *client) []map[string]any {
	t.Helper()
	status, body, _ := c.do("GET", "/api/v1/puzzles/list", nil)
	if status != 200 {
		t.Fatalf("list: %d (%v)", status, body)
	}
	out := []map[string]any{}
	for _, p := range body["puzzles"].([]any) {
		out = append(out, p.(map[string]any))
	}
	return out
}

func tierCounts(list []map[string]any) map[string]int {
	n := map[string]int{}
	for _, p := range list {
		n[p["tier"].(string)]++
	}
	return n
}

// solveListPuzzle plays the whole solution, read as a teacher.
func solveListPuzzle(t *testing.T, pupil, teacher *client, id string) map[string]any {
	t.Helper()
	status, bank, _ := teacher.do("GET", "/api/v1/puzzles/"+id, nil)
	if status != 200 {
		t.Fatalf("read puzzle as teacher: %d", status)
	}
	solution := strings.Fields(bank["moves"].(string))
	played := []string{}
	var last map[string]any
	for i := 0; i < len(solution); i += 2 {
		status, reply, _ := pupil.do("POST", "/api/v1/puzzles/list/"+id+"/attempt",
			map[string]any{"played": played, "move": solution[i]})
		if status != 200 || reply["correct"] != true {
			t.Fatalf("solution move %q refused: %d (%v)", solution[i], status, reply)
		}
		played = append(played, solution[i])
		last = reply
	}
	if last["solved"] != true {
		t.Fatalf("the full solution did not solve %s: %v", id, last)
	}
	return last
}

func setLevel(t *testing.T, d *sql.DB, email, level string) {
	t.Helper()
	if _, err := d.Exec(`UPDATE student SET current_level = ?, fide_rating = NULL
	                     WHERE user_account_id = (SELECT user_account_id FROM user_account WHERE email = ?)`, level, email); err != nil {
		t.Fatal(err)
	}
}

// Twenty puzzles, two in three from the pupil's own level, the rest split
// with the nearer level taking more — and the same twenty on every visit.
func TestTheListIsTwentyMixedToThePupilsLevel(t *testing.T) {
	stub := newPuzzleStub(t, 640) // tops up the Beginner band
	t.Setenv("LICHESS_API_BASE", stub.srv.URL)
	d := newDB(t)
	setLevel(t, d, "penny@jca.ac.th", "Beginner")
	c := &client{t: t, srv: newServerOn(t, d)}
	c.login("penny@jca.ac.th")

	list := puzzleList(t, c)
	if len(list) != 20 {
		t.Fatalf("want 20 puzzles, got %d", len(list))
	}
	n := tierCounts(list)
	if n["beginner"] != 13 || n["intermediate"] != 4 || n["advanced"] != 3 {
		t.Fatalf("mix for a Beginner = %v, want 13/4/3", n)
	}
	for _, p := range list {
		if _, leaked := p["moves"]; leaked {
			t.Fatal("the solution was sent to the client")
		}
	}
	again := puzzleList(t, c)
	for i := range list {
		if list[i]["puzzleId"] != again[i]["puzzleId"] {
			t.Fatalf("the list changed between visits at %d", i)
		}
	}
}

func TestAnAdvancedPupilsListLeansAdvanced(t *testing.T) {
	stub := newPuzzleStub(t, 1400) // tops up the Advanced band
	t.Setenv("LICHESS_API_BASE", stub.srv.URL)
	d := newDB(t)
	setLevel(t, d, "penny@jca.ac.th", "Advanced")
	c := &client{t: t, srv: newServerOn(t, d)}
	c.login("penny@jca.ac.th")

	n := tierCounts(puzzleList(t, c))
	if n["advanced"] != 13 || n["intermediate"] != 4 || n["beginner"] != 3 {
		t.Fatalf("mix for an Advanced pupil = %v, want 13 advanced, 4 intermediate, 3 beginner", n)
	}
}

// The first solve earns the puzzle; playing it again that day is allowed and
// changes nothing.
func TestAListPuzzleCountsOnceAndCanBeReplayed(t *testing.T) {
	stub := newPuzzleStub(t, 640)
	t.Setenv("LICHESS_API_BASE", stub.srv.URL)
	srv := newServer(t)
	pupil := &client{t: t, srv: srv}
	pupil.login("penny@jca.ac.th")
	teacher := &client{t: t, srv: srv}
	teacher.login("serene@jca.ac.th")

	id := puzzleList(t, pupil)[0]["puzzleId"].(string)
	before := puzzlesSolved(t, pupil)
	if reply := solveListPuzzle(t, pupil, teacher, id); reply["firstSolve"] != true {
		t.Fatalf("first solve not marked as such: %v", reply)
	}
	if got := puzzlesSolved(t, pupil); got != before+1 {
		t.Fatalf("puzzles solved = %v, want %v", got, before+1)
	}
	if p := puzzleList(t, pupil)[0]; p["solved"] != true {
		t.Fatalf("solved puzzle not ticked: %v", p)
	}
	if reply := solveListPuzzle(t, pupil, teacher, id); reply["firstSolve"] != false {
		t.Fatalf("a replay was counted again: %v", reply)
	}
	if got := puzzlesSolved(t, pupil); got != before+1 {
		t.Fatalf("a replay changed puzzles solved to %v", got)
	}
}

// The next day, a puzzle solved yesterday is replaced in its place by a new
// one of the same level; everything unsolved stays put and nothing is ticked.
func TestTheNextDayReplacesOnlyTheSolvedOnes(t *testing.T) {
	stub := newPuzzleStub(t, 640)
	t.Setenv("LICHESS_API_BASE", stub.srv.URL)
	d := newDB(t)
	srv := newServerOn(t, d)
	pupil := &client{t: t, srv: srv}
	pupil.login("penny@jca.ac.th")
	teacher := &client{t: t, srv: srv}
	teacher.login("serene@jca.ac.th")

	list := puzzleList(t, pupil)
	solved := list[2]["puzzleId"].(string)
	solveListPuzzle(t, pupil, teacher, solved)
	// As if it were solved yesterday.
	if _, err := d.Exec(`UPDATE puzzle_list SET solved_on = '2000-01-01' WHERE puzzle_id = ?`, solved); err != nil {
		t.Fatal(err)
	}

	next := puzzleList(t, pupil)
	if len(next) != 20 {
		t.Fatalf("want 20 after the refresh, got %d", len(next))
	}
	for i, p := range next {
		if p["solved"] == true {
			t.Fatalf("a tick carried over to the next day at %d", i)
		}
		if i == 2 {
			if p["puzzleId"] == solved {
				t.Fatal("yesterday's solved puzzle was not replaced")
			}
			if p["tier"] != list[2]["tier"] {
				t.Fatalf("replacement is %v, want the same level %v", p["tier"], list[2]["tier"])
			}
			continue
		}
		if p["puzzleId"] != list[i]["puzzleId"] {
			t.Fatalf("an unsolved puzzle moved or changed at %d", i)
		}
	}
}

// A list puzzle is not also handed out as a daily one, and only a pupil's own
// list can be graded.
func TestTheListAndTheDailySetDoNotOverlap(t *testing.T) {
	stub := newPuzzleStub(t, 640)
	t.Setenv("LICHESS_API_BASE", stub.srv.URL)
	c := &client{t: t, srv: newServer(t)}
	c.login("penny@jca.ac.th")

	inList := map[string]bool{}
	for _, p := range puzzleList(t, c) {
		inList[p["puzzleId"].(string)] = true
	}
	daily := dailySet(t, c)
	for _, p := range daily {
		if inList[p["puzzleId"].(string)] {
			t.Fatalf("daily puzzle %v is also in the list", p["puzzleId"])
		}
	}
	status, _, _ := c.do("POST", "/api/v1/puzzles/list/"+daily[0]["puzzleId"].(string)+"/attempt",
		map[string]any{"played": []string{}, "move": "a1a2"})
	if status != 404 {
		t.Fatalf("grading a puzzle outside the list: want 404, got %d", status)
	}
}

func puzzlesSolved(t *testing.T, c *client) float64 {
	t.Helper()
	status, body, _ := c.do("GET", "/api/v1/student/progress", nil)
	if status != 200 {
		t.Fatalf("progress: %d", status)
	}
	n, _ := body["puzzlesSolved"].(float64)
	return n
}
