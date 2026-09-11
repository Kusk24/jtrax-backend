package api_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// dailySet fetches the signed-in pupil's puzzles for today.
func dailySet(t *testing.T, c *client) []map[string]any {
	t.Helper()
	set, _ := dailyReply(t, c)
	return set
}

// dailyReply also returns the envelope, which carries whether the pupil has
// run out of puzzles they have never been set.
func dailyReply(t *testing.T, c *client) ([]map[string]any, map[string]any) {
	t.Helper()
	status, obj, _ := c.do("GET", "/api/v1/puzzles/daily", nil)
	if status != 200 {
		t.Fatalf("daily puzzles: status %d", status)
	}
	raw, _ := obj["puzzles"].([]any)
	set := make([]map[string]any, 0, len(raw))
	for _, row := range raw {
		if m, ok := row.(map[string]any); ok {
			set = append(set, m)
		}
	}
	return set, obj
}

func TestPupilIsSetThreePuzzles(t *testing.T) {
	c := &client{t: t, srv: newServer(t)}
	c.login("penny@jca.ac.th")

	set := dailySet(t, c)
	if len(set) != 3 {
		t.Fatalf("got %d puzzles, want 3", len(set))
	}
	for _, p := range set {
		if p["fen"] == "" || p["puzzleId"] == "" {
			t.Fatalf("puzzle is missing its position: %v", p)
		}
		if p["side"] != "White" && p["side"] != "Black" {
			t.Errorf("side = %v, want White or Black", p["side"])
		}
		if n, _ := p["moveCount"].(float64); n < 1 {
			t.Errorf("moveCount = %v, want at least 1", p["moveCount"])
		}
	}
}

// The whole reason grading is server-side. A solution the client holds is a
// solution the client can read, and these feed streaks a parent sees.
func TestTheSolutionIsNeverSentToThePupil(t *testing.T) {
	srv := newServer(t)
	pupil := &client{t: t, srv: srv}
	pupil.login("penny@jca.ac.th")
	teacher := &client{t: t, srv: srv}
	teacher.login("serene@jca.ac.th")

	status, _, set := pupil.do("GET", "/api/v1/puzzles/daily", nil)
	if status != 200 {
		t.Fatal(status)
	}
	raw, _ := json.Marshal(set)

	// Checking for a field called "moves" is not enough — the answer leaking
	// under some other name is the same leak. So the solution is looked up as
	// a teacher and searched for by value, anywhere in the pupil's response.
	for _, p := range set {
		id := p["puzzleId"].(string)
		status, bank, _ := teacher.do("GET", "/api/v1/puzzles/"+id, nil)
		if status != 200 {
			t.Fatalf("read puzzle %s as teacher: %d", id, status)
		}
		solution, _ := bank["moves"].(string)
		if solution == "" {
			t.Fatalf("puzzle %s has no stored solution", id)
		}
		if strings.Contains(string(raw), solution) {
			t.Fatalf("puzzle %s: the solution %q appears in what the pupil is sent: %s", id, solution, raw)
		}
		// The first move alone is enough to give a one-mover away.
		if first := strings.Fields(solution)[0]; strings.Contains(string(raw), first) {
			t.Fatalf("puzzle %s: the first solution move %q appears in what the pupil is sent", id, first)
		}
		if _, ok := p["moves"]; ok {
			t.Fatalf("puzzle carries a moves field: %v", p)
		}
	}
}

// A pupil must not be able to read solutions through the authoring resource.
func TestPupilsCannotReachThePuzzleBank(t *testing.T) {
	srv := newServer(t)
	pupil := &client{t: t, srv: srv}
	pupil.login("penny@jca.ac.th")

	if status, _, _ := pupil.do("GET", "/api/v1/puzzles", nil); status != 403 {
		t.Errorf("pupil listed the puzzle bank: status %d, want 403", status)
	}
	if status, _, _ := pupil.do("POST", "/api/v1/puzzles", map[string]any{
		"fen": "7k/8/8/8/8/8/6QK/8 w - - 0 1", "moves": "g2g7",
	}); status != 403 {
		t.Errorf("pupil authored a puzzle: status %d, want 403", status)
	}

	// A teacher may, because they set the lesson.
	teacher := &client{t: t, srv: srv}
	teacher.login("serene@jca.ac.th")
	if status, _, _ := teacher.do("GET", "/api/v1/puzzles", nil); status != 200 {
		t.Errorf("teacher cannot read the puzzle bank: status %d", status)
	}
}

func TestGradingAcceptsTheSolutionAndRejectsAnythingElse(t *testing.T) {
	srv := newServer(t)
	pupil := &client{t: t, srv: srv}
	pupil.login("penny@jca.ac.th")
	teacher := &client{t: t, srv: srv}
	teacher.login("serene@jca.ac.th")

	set := dailySet(t, pupil)
	id := set[0]["puzzleId"].(string)

	// The teacher can see the solution; the pupil cannot. That asymmetry is
	// what lets this test know the right answer.
	status, bank, _ := teacher.do("GET", "/api/v1/puzzles/"+id, nil)
	if status != 200 {
		t.Fatalf("read puzzle as teacher: %d", status)
	}
	solution := strings.Fields(bank["moves"].(string))

	// A wrong move is refused, and refused without a hint.
	status, wrong, _ := pupil.do("POST", "/api/v1/puzzles/"+id+"/attempt",
		map[string]any{"played": []string{}, "move": "a1a2"})
	if status != 200 {
		t.Fatalf("attempt: status %d", status)
	}
	if wrong["correct"] != false {
		t.Errorf("a1a2 was accepted for %s", id)
	}
	raw, _ := json.Marshal(wrong)
	if strings.Contains(string(raw), solution[0]) {
		t.Fatalf("a rejection revealed the answer: %s", raw)
	}

	// The real move is accepted, and the server plays the opponent's reply.
	played := []string{}
	for i := 0; i < len(solution); i += 2 {
		status, ok, _ := pupil.do("POST", "/api/v1/puzzles/"+id+"/attempt",
			map[string]any{"played": played, "move": solution[i]})
		if status != 200 || ok["correct"] != true {
			t.Fatalf("solution move %q refused: status %d (%v)", solution[i], status, ok)
		}
		played = append(played, solution[i])
		if i+2 >= len(solution) {
			if ok["solved"] != true {
				t.Fatalf("final move did not solve the puzzle: %v", ok)
			}
		} else if ok["reply"] != solution[i+1] {
			t.Errorf("reply = %v, want %v", ok["reply"], solution[i+1])
		}
	}

	// And the solve is recorded.
	for _, p := range dailySet(t, pupil) {
		if p["puzzleId"] == id && p["solved"] != true {
			t.Fatalf("solved puzzle not recorded: %v", p)
		}
	}
}

// Wrong guesses are counted — a puzzle solved first time is worth more than one
// brute forced, and the difference is what tells a teacher who is struggling.
func TestWrongGuessesAreCounted(t *testing.T) {
	c := &client{t: t, srv: newServer(t)}
	c.login("penny@jca.ac.th")
	id := dailySet(t, c)[0]["puzzleId"].(string)

	for _, bad := range []string{"a1a2", "h1h2", "b1b2"} {
		c.do("POST", "/api/v1/puzzles/"+id+"/attempt", map[string]any{"played": []string{}, "move": bad})
	}
	for _, p := range dailySet(t, c) {
		if p["puzzleId"] == id {
			if n, _ := p["wrongMoves"].(float64); n != 3 {
				t.Fatalf("wrongMoves = %v, want 3", p["wrongMoves"])
			}
		}
	}
}

// Refreshing must not reroll a puzzle the pupil has just failed.
func TestTheDailySetIsStable(t *testing.T) {
	c := &client{t: t, srv: newServer(t)}
	c.login("penny@jca.ac.th")

	first := dailySet(t, c)
	second := dailySet(t, c)
	if len(first) != len(second) {
		t.Fatalf("set size changed: %d then %d", len(first), len(second))
	}
	for i := range first {
		if first[i]["puzzleId"] != second[i]["puzzleId"] {
			t.Fatalf("puzzle %d changed on refresh: %v -> %v", i, first[i]["puzzleId"], second[i]["puzzleId"])
		}
	}
}

// Submitting against a puzzle that was never set is how a pupil would grind the
// whole bank looking for answers.
func TestYouCanOnlyAttemptTodaysPuzzles(t *testing.T) {
	srv := newServer(t)
	pupil := &client{t: t, srv: srv}
	pupil.login("penny@jca.ac.th")
	teacher := &client{t: t, srv: srv}
	teacher.login("serene@jca.ac.th")

	assigned := map[string]bool{}
	for _, p := range dailySet(t, pupil) {
		assigned[p["puzzleId"].(string)] = true
	}
	_, _, bank := teacher.do("GET", "/api/v1/puzzles", nil)
	other := ""
	for _, p := range bank {
		if id, _ := p["puzzle_id"].(string); id != "" && !assigned[id] {
			other = id
			break
		}
	}
	if other == "" {
		t.Skip("every puzzle happens to be assigned")
	}
	status, _, _ := pupil.do("POST", "/api/v1/puzzles/"+other+"/attempt",
		map[string]any{"played": []string{}, "move": "e2e4"})
	if status != 404 {
		t.Fatalf("attempted an unassigned puzzle: status %d, want 404", status)
	}
}

// A teacher's typo must be a 400 at the boundary, not a pupil stuck forever on
// a board that cannot be solved.
func TestAuthoringRejectsAnImpossibleSolution(t *testing.T) {
	c := &client{t: t, srv: newServer(t)}
	c.login("serene@jca.ac.th")

	mateInOne := "6k1/5ppp/8/8/8/8/5PPP/R5K1 w - - 0 1"
	status, body, _ := c.do("POST", "/api/v1/puzzles", map[string]any{
		"fen": mateInOne, "moves": "a1a9", "rating": 500, "source": "JCA",
	})
	if status != 400 {
		t.Fatalf("impossible solution accepted: status %d (%v)", status, body)
	}

	status, _, _ = c.do("POST", "/api/v1/puzzles", map[string]any{
		"fen": mateInOne, "moves": "a1a8", "rating": 500, "source": "JCA",
	})
	if status != 201 {
		t.Fatalf("a legal solution was refused: status %d", status)
	}
}

func TestOnlyStudentsAreSetPuzzles(t *testing.T) {
	srv := newServer(t)
	for _, email := range []string{"serene@jca.ac.th", "sandy01234@gmail.com", "admin@jca.ac.th"} {
		c := &client{t: t, srv: srv}
		c.login(email)
		if status, _, _ := c.do("GET", "/api/v1/puzzles/daily", nil); status != 403 {
			t.Errorf("%s was set puzzles: status %d, want 403", email, status)
		}
	}
}

func TestSigningInIsRequiredForPuzzles(t *testing.T) {
	srv := newServer(t)
	anon := &client{t: t, srv: srv}
	if status, _, _ := anon.do("GET", "/api/v1/puzzles/daily", nil); status != 401 {
		t.Errorf("anonymous daily set: status %d, want 401", status)
	}
	if status, _, _ := anon.do("POST", "/api/v1/puzzles/x/attempt", map[string]any{"move": "e2e4"}); status != 401 {
		t.Errorf("anonymous attempt: status %d, want 401", status)
	}
}

// A pupil is never set the same puzzle twice, and is told when that means
// there is nothing left rather than being handed a silently empty day.
func TestPuzzlesAreNeverRepeatedAndExhaustionIsSaidOutLoud(t *testing.T) {
	d := newDB(t)
	srv := newServerOn(t, d)
	pupil := &client{t: t, srv: srv}
	pupil.login("penny@jca.ac.th")

	// Walk far enough to empty the seeded bank, back-dating each day's set so
	// the next request counts as a new day.
	seen := map[string]bool{}
	var last map[string]any
	for day := 0; day < 40; day++ {
		set, obj := dailyReply(t, pupil)
		last = obj
		for _, p := range set {
			id, _ := p["puzzleId"].(string)
			if seen[id] {
				t.Fatalf("puzzle %s was set twice (day %d)", id, day)
			}
			seen[id] = true
		}
		if len(set) == 0 {
			break
		}
		if _, err := d.Exec(
			`UPDATE puzzle_attempt SET assigned_on = date('now', ?)
			  WHERE student_id = 'stu_penny' AND assigned_on = date('now')`,
			fmt.Sprintf("-%d days", day+1)); err != nil {
			t.Fatal(err)
		}
	}

	if len(seen) == 0 {
		t.Fatal("no puzzles were ever set")
	}
	if exhausted, _ := last["exhausted"].(bool); !exhausted {
		t.Errorf("the bank is spent but exhausted = %v (unseen = %v)", last["exhausted"], last["unseen"])
	}
	if n, _ := last["unseen"].(float64); n != 0 {
		t.Errorf("unseen = %v, want 0 once every puzzle has been set", last["unseen"])
	}
}

// Puzzles come from inside the pupil's rating band while the band has any
// left. Penny has no FIDE rating, so she is treated as 800.
func TestPuzzlesComeFromTheRatingBandFirst(t *testing.T) {
	pupil := &client{t: t, srv: newServer(t)}
	pupil.login("penny@jca.ac.th")

	for _, p := range dailySet(t, pupil) {
		r, _ := p["rating"].(float64)
		if diff := r - 800; diff > 250 || diff < -250 {
			t.Errorf("puzzle rated %v is outside the ±250 band around 800", r)
		}
	}
}
