package api_test

import "testing"

// An Advanced pupil with no FIDE rating is set Advanced puzzles, not the 800
// that stood in for every unrated child.
func TestDailyPuzzlesFollowThePupilsLevel(t *testing.T) {
	d := newDB(t)
	if _, err := d.Exec(`UPDATE student SET current_level = 'Advanced', fide_rating = NULL
	                     WHERE student_id = (SELECT s.student_id FROM student s JOIN user_account u ON u.user_account_id = s.user_account_id WHERE u.email = 'penny@jca.ac.th')`); err != nil {
		t.Fatal(err)
	}
	c := &client{t: t, srv: newServerOn(t, d)}
	c.login("penny@jca.ac.th")

	for _, p := range dailySet(t, c) {
		if r, _ := p["rating"].(float64); r < 1200 {
			t.Fatalf("an Advanced pupil was set a %v-rated puzzle", r)
		}
	}
}

// A level changed today takes effect today: untouched puzzles outside it are
// swapped, while one the pupil has already started stays.
func TestAChangedLevelSwapsTodaysUntouchedPuzzles(t *testing.T) {
	d := newDB(t)
	c := &client{t: t, srv: newServerOn(t, d)}
	c.login("penny@jca.ac.th")

	before := dailySet(t, c)
	started := before[0]["puzzleId"].(string)
	if status, _, _ := c.do("POST", "/api/v1/puzzles/"+started+"/open", nil); status != 200 {
		t.Fatalf("open: %d", status)
	}
	if _, err := d.Exec(`UPDATE student SET current_level = 'Advanced', fide_rating = NULL
	                     WHERE student_id = (SELECT s.student_id FROM student s JOIN user_account u ON u.user_account_id = s.user_account_id WHERE u.email = 'penny@jca.ac.th')`); err != nil {
		t.Fatal(err)
	}

	after := dailySet(t, c)
	kept := false
	for _, p := range after {
		id := p["puzzleId"].(string)
		if id == started {
			kept = true
			continue
		}
		if r, _ := p["rating"].(float64); r < 1200 {
			t.Fatalf("an untouched %v-rated puzzle was not swapped", r)
		}
	}
	if !kept {
		t.Fatal("a puzzle the pupil had already opened was swapped out")
	}
}
