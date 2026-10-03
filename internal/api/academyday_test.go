package api_test

import (
	"testing"
	"time"

	"github.com/Kusk24/jtrax-backend/internal/academytime"
)

// At 06:30 in Bangkok, UTC is still on the previous day. A pupil opening the
// app before school must get today's puzzles and see today on their strip —
// not yesterday's, which is what a server whose clock is UTC used to give them.
func TestAnEarlyMorningPupilGetsTheAcademysDay(t *testing.T) {
	early, _ := time.Parse(time.RFC3339, "2026-09-20T23:30:00Z") // 06:30 on the 21st in Bangkok
	defer academytime.Freeze(early)()

	d := newDB(t)
	c := &client{t: t, srv: newServerOn(t, d)}
	c.login("penny@jca.ac.th")

	if status, obj, _ := c.do("GET", "/api/v1/puzzles/daily", nil); status != 200 {
		t.Fatalf("daily puzzles: %d (%v)", status, obj)
	}
	var assigned string
	if err := d.QueryRow(`SELECT MAX(assigned_on) FROM puzzle_attempt WHERE student_id = 'stu_penny'`).
		Scan(&assigned); err != nil {
		t.Fatal(err)
	}
	if assigned != "2026-09-21" {
		t.Fatalf("today's set is dated %s, want the academy's 2026-09-21", assigned)
	}

	sum := summaryFor(t, c)
	days, _ := sum["days"].([]any)
	if len(days) == 0 {
		t.Fatalf("no days in the practice strip: %v", sum)
	}
	last, _ := days[len(days)-1].(map[string]any)
	if last["date"] != "2026-09-21" {
		t.Fatalf("the strip ends on %v, want the academy's 2026-09-21", last["date"])
	}
}

// A streak that lapsed the day before yesterday has lapsed, whatever the hour.
// The streak compared "now" — with its hours — against rows dated at
// midnight, so at dawn the two-day gap measured under two days and a lapsed
// streak was still shown as alive.
func TestALapsedStreakHasLapsedAtDawn(t *testing.T) {
	early, _ := time.Parse(time.RFC3339, "2026-09-20T23:30:00Z") // 06:30 on the 21st in Bangkok
	defer academytime.Freeze(early)()

	d := newDB(t)
	srv := newServerOn(t, d)
	if _, err := d.Exec(`DELETE FROM practice_activity WHERE student_id = 'stu_penny'`); err != nil {
		t.Fatal(err)
	}
	for _, back := range []int{2, 3, 4} {
		practised(t, d, "stu_penny", back, 3)
	}
	penny := &client{t: t, srv: srv}
	penny.login("penny@jca.ac.th")
	if got, _ := summaryFor(t, penny)["streak"].(float64); got != 0 {
		t.Fatalf("streak = %v at dawn two days after the last practice, want 0", got)
	}
}
