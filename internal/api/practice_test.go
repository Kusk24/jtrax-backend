package api_test

import (
	"database/sql"
	"fmt"
	"testing"
)

// practised writes a practice row `back` days ago.
func practised(t *testing.T, d *sql.DB, studentID string, back, puzzles int) {
	t.Helper()
	if _, err := d.Exec(
		`INSERT OR REPLACE INTO practice_activity
		   (activity_id, student_id, activity_date, minutes_practiced, puzzles_completed, points_earned, streak_count)
		 VALUES (?,?,date('now', ?),0,?,0,0)`,
		fmt.Sprintf("act_%s_%d", studentID, back), studentID,
		fmt.Sprintf("-%d days", back), puzzles); err != nil {
		t.Fatal(err)
	}
}

func summaryFor(t *testing.T, c *client) map[string]any {
	t.Helper()
	status, obj, _ := c.do("GET", "/api/v1/practice/summary", nil)
	if status != 200 {
		t.Fatalf("practice summary: status %d (%v)", status, obj)
	}
	return obj
}

// The streak is the number of days in a row ending today — not a stored
// number, and not the count of practice rows.
func TestStreakCountsConsecutiveDaysEndingToday(t *testing.T) {
	d := newDB(t)
	srv := newServerOn(t, d)
	// The seed leaves an old row for Penny; the run below must not reach it.
	if _, err := d.Exec(`DELETE FROM practice_activity WHERE student_id = 'stu_penny'`); err != nil {
		t.Fatal(err)
	}
	for _, back := range []int{0, 1, 2} {
		practised(t, d, "stu_penny", back, 3)
	}
	// And a much older cluster, which is a different run entirely.
	practised(t, d, "stu_penny", 9, 3)
	practised(t, d, "stu_penny", 10, 3)

	penny := &client{t: t, srv: srv}
	penny.login("penny@jca.ac.th")
	if got, _ := summaryFor(t, penny)["streak"].(float64); got != 3 {
		t.Fatalf("streak = %v, want 3 (today, yesterday, the day before)", got)
	}
}

// A gap ends it. This is the case the stored column could never see: Penny
// showed twelve days having last practised in May.
func TestAMissedDayBreaksTheStreak(t *testing.T) {
	d := newDB(t)
	srv := newServerOn(t, d)
	if _, err := d.Exec(`DELETE FROM practice_activity WHERE student_id = 'stu_penny'`); err != nil {
		t.Fatal(err)
	}
	// Practised for a week, but not for the last three days.
	for back := 3; back < 10; back++ {
		practised(t, d, "stu_penny", back, 3)
	}
	penny := &client{t: t, srv: srv}
	penny.login("penny@jca.ac.th")
	if got, _ := summaryFor(t, penny)["streak"].(float64); got != 0 {
		t.Fatalf("streak = %v, want 0 — the last practice was three days ago", got)
	}
}

// Yesterday still counts, so a pupil who has not opened the app *yet today*
// is not told their streak is gone before the day is out.
func TestYesterdayStillCountsButTheDayBeforeDoesNot(t *testing.T) {
	for _, tc := range []struct {
		name string
		back []int
		want float64
	}{
		{"ending yesterday", []int{1, 2, 3}, 3},
		{"ending the day before yesterday", []int{2, 3, 4}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newDB(t)
			srv := newServerOn(t, d)
			if _, err := d.Exec(`DELETE FROM practice_activity WHERE student_id = 'stu_penny'`); err != nil {
				t.Fatal(err)
			}
			for _, back := range tc.back {
				practised(t, d, "stu_penny", back, 3)
			}
			penny := &client{t: t, srv: srv}
			penny.login("penny@jca.ac.th")
			if got, _ := summaryFor(t, penny)["streak"].(float64); got != tc.want {
				t.Fatalf("streak = %v, want %v", got, tc.want)
			}
		})
	}
}

// The week strip is real days, not the first N cells lit from the streak.
func TestTheWeekStripReportsTheDaysActuallyPractised(t *testing.T) {
	d := newDB(t)
	srv := newServerOn(t, d)
	if _, err := d.Exec(`DELETE FROM practice_activity WHERE student_id = 'stu_penny'`); err != nil {
		t.Fatal(err)
	}
	practised(t, d, "stu_penny", 0, 3)
	practised(t, d, "stu_penny", 4, 2)

	penny := &client{t: t, srv: srv}
	penny.login("penny@jca.ac.th")
	days, _ := summaryFor(t, penny)["days"].([]any)
	if len(days) != 7 {
		t.Fatalf("got %d days, want 7", len(days))
	}
	// Oldest first, so today is last and four days ago is index 2.
	on := make([]bool, 7)
	for i, row := range days {
		m, _ := row.(map[string]any)
		on[i], _ = m["practised"].(bool)
	}
	want := []bool{false, false, true, false, false, false, true}
	for i := range want {
		if on[i] != want[i] {
			t.Fatalf("day pattern %v, want %v", on, want)
		}
	}
}

// Solving a puzzle is what writes the practice row — the browser is never
// asked, and never believed.
func TestSolvingAPuzzleRecordsPracticeOnTheServer(t *testing.T) {
	d := newDB(t)
	srv := newServerOn(t, d)
	if _, err := d.Exec(`DELETE FROM practice_activity WHERE student_id = 'stu_penny'`); err != nil {
		t.Fatal(err)
	}
	penny := &client{t: t, srv: srv}
	penny.login("penny@jca.ac.th")

	if got, _ := summaryFor(t, penny)["streak"].(float64); got != 0 {
		t.Fatalf("streak before practising = %v, want 0", got)
	}

	set := dailySet(t, penny)
	id, _ := set[0]["puzzleId"].(string)
	var solution string
	if err := d.QueryRow(`SELECT moves FROM puzzle WHERE puzzle_id = ?`, id).Scan(&solution); err != nil {
		t.Fatal(err)
	}
	first := solution
	for i, ch := range solution {
		if ch == ' ' {
			first = solution[:i]
			break
		}
	}
	status, obj, _ := penny.do("POST", "/api/v1/puzzles/"+id+"/attempt",
		map[string]any{"move": first, "played": []string{}})
	if status != 200 || obj["correct"] != true {
		t.Fatalf("solving: status %d, %v", status, obj)
	}

	sum := summaryFor(t, penny)
	if got, _ := sum["streak"].(float64); got != 1 {
		t.Fatalf("streak after solving = %v, want 1", got)
	}
	if got, _ := sum["todaySolved"].(float64); got != 1 {
		t.Errorf("todaySolved = %v, want 1", got)
	}
	var n int
	if err := d.QueryRow(`SELECT COUNT(*) FROM practice_activity
	                      WHERE student_id = 'stu_penny' AND activity_date = date('now')`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("practice rows for today = %d, want 1", n)
	}
}

// Practice is a pupil's own business; the summary is scoped to the caller.
func TestOnlyAStudentHasAPracticeSummary(t *testing.T) {
	srv := newServer(t)
	for _, email := range []string{"sandy01234@gmail.com", "admin@jca.ac.th"} {
		c := &client{t: t, srv: srv}
		c.login(email)
		if status, _, _ := c.do("GET", "/api/v1/practice/summary", nil); status != 403 {
			t.Errorf("%s practice summary: want 403, got %d", email, status)
		}
	}
}
