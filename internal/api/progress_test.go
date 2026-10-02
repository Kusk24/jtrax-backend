package api_test

import (
	"database/sql"
	"fmt"
	"testing"
)

/* The student portal's points: worked out from what was recorded, never
   posted by the browser. */

// clearPennysRecord empties the seed's history for Penny so a test counts
// only what it adds.
func clearPennysRecord(t *testing.T, d *sql.DB) {
	t.Helper()
	for _, q := range []string{
		`DELETE FROM practice_activity WHERE student_id = 'stu_penny'`,
		`DELETE FROM puzzle_attempt WHERE student_id = 'stu_penny'`,
		`DELETE FROM solo_game WHERE student_id = 'stu_penny'`,
		`DELETE FROM game_move WHERE game_room_id IN (SELECT g.game_room_id FROM game_room g JOIN student s
		   ON s.user_account_id IN (g.white_account_id, g.black_account_id) WHERE s.student_id = 'stu_penny')`,
		`DELETE FROM game_challenge WHERE game_room_id IN (SELECT g.game_room_id FROM game_room g JOIN student s
		   ON s.user_account_id IN (g.white_account_id, g.black_account_id) WHERE s.student_id = 'stu_penny')`,
		`DELETE FROM game_room WHERE game_room_id IN (SELECT g.game_room_id FROM game_room g JOIN student s
		   ON s.user_account_id IN (g.white_account_id, g.black_account_id) WHERE s.student_id = 'stu_penny')`,
	} {
		if _, err := d.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
}

// attempt records a daily or free-play puzzle for Penny on a day back from today.
func attempt(t *testing.T, d *sql.DB, n, back int, source string, solved bool) {
	t.Helper()
	var puzzle string
	if err := d.QueryRow(`SELECT puzzle_id FROM puzzle ORDER BY puzzle_id LIMIT 1 OFFSET ?`, n).Scan(&puzzle); err != nil {
		t.Fatal(err)
	}
	s := 0
	if solved {
		s = 1
	}
	if _, err := d.Exec(`INSERT INTO puzzle_attempt
		(puzzle_attempt_id, student_id, puzzle_id, assigned_on, solved, source)
		VALUES (?, 'stu_penny', ?, ?, ?, ?)`,
		fmt.Sprintf("pa_%d_%d", n, back), puzzle, academyDay(-back), s, source); err != nil {
		t.Fatal(err)
	}
}

func progressOf(t *testing.T, c *client) map[string]any {
	t.Helper()
	status, obj, _ := c.do("GET", "/api/v1/student/progress", nil)
	if status != 200 {
		t.Fatalf("progress: %d (%v)", status, obj)
	}
	return obj
}

func num(m map[string]any, path ...string) int {
	var v any = m
	for _, k := range path {
		v = v.(map[string]any)[k]
	}
	f, _ := v.(float64)
	return int(f)
}

func TestPointsCountPuzzlesGamesAndDailyChallenges(t *testing.T) {
	d := newDB(t)
	srv := newServerOn(t, d)
	clearPennysRecord(t, d)

	// Yesterday's daily challenge done: 3 solved puzzles (+15) and the bonus (+10).
	attempt(t, d, 0, 1, "daily", true)
	attempt(t, d, 1, 1, "daily", true)
	attempt(t, d, 2, 1, "daily", true)
	// Today: one of three solved (+5), not complete.
	attempt(t, d, 3, 0, "daily", true)
	attempt(t, d, 4, 0, "daily", false)
	attempt(t, d, 5, 0, "daily", false)
	// A free-play puzzle solved (+5).
	attempt(t, d, 6, 0, "free", true)
	// Two games against the computer, one won and one lost (+20), and one
	// abandoned without a result, which earns nothing.
	for i, res := range []any{"1-0", "0-1", nil} {
		if _, err := d.Exec(`INSERT INTO solo_game (solo_game_id, student_id, opponent, student_side, result)
			VALUES (?, 'stu_penny', 'novice', 'white', ?)`, fmt.Sprintf("sg_%d", i), res); err != nil {
			t.Fatal(err)
		}
	}

	penny := &client{t: t, srv: srv}
	penny.login("penny@jca.ac.th")
	p := progressOf(t, penny)

	if got := num(p, "puzzlesSolved"); got != 5 {
		t.Fatalf("puzzlesSolved: want 5, got %d", got)
	}
	if got, won := num(p, "gamesPlayed"), num(p, "gamesWon"); got != 2 || won != 1 {
		t.Fatalf("games: want 2 played 1 won, got %d / %d", got, won)
	}
	if got := num(p, "challengesCompleted"); got != 1 {
		t.Fatalf("challengesCompleted: want 1, got %d", got)
	}
	// 5 puzzles × 5 + 2 games × 10 + 1 challenge × 10 = 55
	if got := num(p, "points", "total"); got != 55 {
		t.Fatalf("points: want 55, got %d (%v)", got, p["points"])
	}
	if lvl, next := num(p, "level", "level"), num(p, "level", "toNext"); lvl != 1 || next != 35 {
		t.Fatalf("level: want 1 with 35 to go, got %d / %d", lvl, next)
	}

	days := p["challenges"].([]any)
	if len(days) != 2 {
		t.Fatalf("challenge days: want 2, got %d", len(days))
	}
	today, yesterday := days[0].(map[string]any), days[1].(map[string]any)
	if today["complete"] != false || num(today, "solved") != 1 || num(today, "points") != 5 {
		t.Fatalf("today's challenge: %v", today)
	}
	if yesterday["complete"] != true || num(yesterday, "points") != 25 {
		t.Fatalf("yesterday's challenge: %v", yesterday)
	}
}

func TestLongestStreakOutlivesTheCurrentOne(t *testing.T) {
	d := newDB(t)
	srv := newServerOn(t, d)
	clearPennysRecord(t, d)
	// A four-day run a while ago, then today and yesterday.
	for _, back := range []int{0, 1, 10, 11, 12, 13} {
		practised(t, d, "stu_penny", back, 3)
	}
	penny := &client{t: t, srv: srv}
	penny.login("penny@jca.ac.th")
	p := progressOf(t, penny)
	if cur, long := num(p, "streak", "current"), num(p, "streak", "longest"); cur != 2 || long != 4 {
		t.Fatalf("streak: want current 2 longest 4, got %d / %d", cur, long)
	}
	if n := len(p["practisedDays"].([]any)); n != 6 {
		t.Fatalf("calendar days: want 6, got %d", n)
	}
}

func TestProgressIsOnlyForStudents(t *testing.T) {
	c := &client{t: t, srv: newServer(t)}
	c.login("serene@jca.ac.th")
	if status, _, _ := c.do("GET", "/api/v1/student/progress", nil); status != 403 {
		t.Fatalf("a teacher reading progress: want 403, got %d", status)
	}
}

// A challenge game on the board says who it was against, which side the pupil
// had, the clock and that it came from a challenge.
func TestHistoryDescribesAChallengeGame(t *testing.T) {
	penny, uri, _, uriID := twoStudents(t)
	_, sent, _ := penny.do("POST", "/api/v1/challenges", map[string]any{
		"studentId": uriID, "clockLimit": 600, "clockIncrement": 5})
	_, acc, _ := uri.do("POST", "/api/v1/challenges/"+sent["challengeId"].(string)+"/accept", nil)
	roomID := acc["gameRoomId"].(string)
	if status, out, _ := uri.do("POST", "/api/v1/game-rooms/"+roomID+"/resign", nil); status != 200 {
		t.Fatalf("resign: %d (%v)", status, out)
	}

	_, obj, _ := penny.do("GET", "/api/v1/students/stu_penny/history", nil)
	for _, raw := range obj["history"].([]any) {
		e := raw.(map[string]any)
		if e["id"] != roomID {
			continue
		}
		if e["gameType"] != "challenge" || e["side"] != "white" || e["opponent"] == "" ||
			e["result"] != "1-0" || num(e, "points") != 10 {
			t.Fatalf("history entry: %v", e)
		}
		return
	}
	t.Fatalf("the challenge game is not in Penny's history")
}

// A game against the computer, recorded from the browser, can be read back
// with its moves by the pupil — and by nobody unrelated.
func TestASoloGameCanBeReplayedByItsPlayer(t *testing.T) {
	penny, uri, _, _ := twoStudents(t)
	if status, out, _ := penny.do("POST", "/api/v1/games/solo", map[string]any{
		"opponent": "novice", "moves": []string{"e2e4", "e7e5"}, "result": "0-1", "reason": "resignation"}); status != 201 {
		t.Fatalf("record: %d (%v)", status, out)
	}
	_, obj, _ := penny.do("GET", "/api/v1/students/stu_penny/history", nil)
	var id string
	for _, raw := range obj["history"].([]any) {
		if e := raw.(map[string]any); e["kind"] == "solo" {
			id = e["id"].(string)
			if e["gameType"] != "computer" || e["side"] != "white" || num(e, "points") != 10 {
				t.Fatalf("solo history entry: %v", e)
			}
			break
		}
	}
	if id == "" {
		t.Fatal("the solo game is not in history")
	}
	status, game, _ := penny.do("GET", "/api/v1/games/solo/"+id, nil)
	if status != 200 || len(game["moves"].([]any)) != 2 {
		t.Fatalf("replay: %d (%v)", status, game)
	}
	if status, _, _ := uri.do("GET", "/api/v1/games/solo/"+id, nil); status != 404 {
		t.Fatalf("another pupil reading it: want 404, got %d", status)
	}
}
