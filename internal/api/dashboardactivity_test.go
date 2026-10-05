package api_test

import (
	"testing"
	"time"
)

func TestDashboardActivity(t *testing.T) {
	d := newDB(t)
	srv := newServerOn(t, d)
	admin := &client{t: t, srv: srv}
	admin.login("admin@jca.ac.th")

	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := d.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	var penny, uri, puzzleA, puzzleB string
	d.QueryRow(`SELECT user_account_id FROM student WHERE student_id = 'stu_penny'`).Scan(&penny)
	d.QueryRow(`SELECT user_account_id FROM student WHERE student_id = 'stu_uri'`).Scan(&uri)
	d.QueryRow(`SELECT puzzle_id FROM puzzle ORDER BY puzzle_id LIMIT 1`).Scan(&puzzleA)
	d.QueryRow(`SELECT puzzle_id FROM puzzle ORDER BY puzzle_id LIMIT 1 OFFSET 1`).Scan(&puzzleB)
	now := time.Now().UTC().Format("2006-01-02 15:04:05")

	// The office opened a game today; the students opened a challenge of their own.
	mustExec(`INSERT INTO game_room (game_room_id, code, created_by, status, white_account_id, black_account_id, fen, started_at)
	          VALUES ('gr_office', 'OFF001', 'usr_admin1', 'Active', ?, ?, 'x', ?)`, penny, uri, now)
	mustExec(`INSERT INTO game_room (game_room_id, code, created_by, status, white_account_id, black_account_id, fen)
	          VALUES ('gr_chal', 'CHAL01', ?, 'Open', ?, ?, 'x')`, uri, uri, penny)

	// Uri practised three days running: consistent.
	for i := 0; i < 3; i++ {
		mustExec(`INSERT INTO practice_activity (activity_id, student_id, activity_date) VALUES (?, 'stu_uri', ?)`,
			"pa_"+academyDay(-i), academyDay(-i))
	}
	// Penny solved puzzles on two earlier days and played today: three days.
	mustExec(`INSERT INTO puzzle_attempt (puzzle_attempt_id, student_id, puzzle_id, assigned_on, solved) VALUES ('pz1', 'stu_penny', ?, ?, 1)`,
		puzzleA, academyDay(-2))
	mustExec(`INSERT INTO puzzle_attempt (puzzle_attempt_id, student_id, puzzle_id, assigned_on, solved) VALUES ('pz2', 'stu_penny', ?, ?, 1)`,
		puzzleB, academyDay(-4))

	status, a, _ := admin.do("GET", "/api/v1/dashboard/activity", nil)
	if status != 200 {
		t.Fatalf("activity: %d %v", status, a)
	}
	if a["gamesOpened"] != float64(1) {
		t.Errorf("gamesOpened: only the office's game counts, got %v", a["gamesOpened"])
	}
	if a["consistentPlayers"] != float64(2) {
		t.Errorf("consistentPlayers: want Uri and Penny, got %v", a["consistentPlayers"])
	}

	// A date three weeks back reads that week: nobody played, and the
	// office's game today is not in it.
	status, past, _ := admin.do("GET", "/api/v1/dashboard/activity?date="+academyDay(-21), nil)
	if status != 200 || past["gamesOpened"] != float64(0) || past["consistentPlayers"] != float64(0) {
		t.Errorf("three weeks back: %d %v, want 0 and 0", status, past)
	}
	if status, _, _ := admin.do("GET", "/api/v1/dashboard/activity?date=soon", nil); status != 400 {
		t.Errorf("bad date: want 400, got %d", status)
	}

	parent := &client{t: t, srv: srv}
	parent.login("sandy01234@gmail.com")
	if status, _, _ := parent.do("GET", "/api/v1/dashboard/activity", nil); status != 403 {
		t.Errorf("parent: want 403, got %d", status)
	}
}
