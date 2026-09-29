package api_test

import (
	"testing"

	"github.com/Kusk24/jtrax-backend/internal/api"
)

func tournamentStatusOf(t *testing.T, c *client, id string) string {
	t.Helper()
	_, row, _ := c.do("GET", "/api/v1/tournaments/"+id, nil)
	s, _ := row["tournament_status"].(string)
	return s
}

// Saving a tournament gives it the status its dates say.
func TestTournamentStatusFollowsItsDatesOnSave(t *testing.T) {
	c := &client{t: t, srv: newServer(t)}
	c.login("admin@jca.ac.th")
	cases := []struct {
		start, end, want string
	}{
		{academyDay(5), academyDay(6), "Upcoming"},
		{academyDay(-1), academyDay(1), "Ongoing"},
		{academyDay(0), "", "Ongoing"},
		{academyDay(-5), academyDay(-3), "Completed"},
	}
	for _, tc := range cases {
		body := map[string]any{"name": "Open", "tournament_status": "Upcoming", "start_date": tc.start}
		if tc.end != "" {
			body["end_date"] = tc.end
		}
		_, trn, _ := c.do("POST", "/api/v1/tournaments", body)
		if got := tournamentStatusOf(t, c, trn["tournament_id"].(string)); got != tc.want {
			t.Errorf("%s–%s: want %s, got %s", tc.start, tc.end, tc.want, got)
		}
	}
}

// The timer moves a tournament on as the days pass.
func TestTournamentStatusMovesOnWithTheDays(t *testing.T) {
	d := newDB(t)
	c := &client{t: t, srv: newServerOn(t, d)}
	c.login("admin@jca.ac.th")
	_, trn, _ := c.do("POST", "/api/v1/tournaments", map[string]any{
		"name": "Open", "start_date": academyDay(2), "end_date": academyDay(3),
	})
	id := trn["tournament_id"].(string)
	for _, step := range []struct{ day, want string }{
		{academyDay(1), "Upcoming"}, {academyDay(2), "Ongoing"}, {academyDay(3), "Ongoing"}, {academyDay(4), "Completed"},
	} {
		if err := api.SyncTournamentStatus(d, step.day); err != nil {
			t.Fatal(err)
		}
		if got := tournamentStatusOf(t, c, id); got != step.want {
			t.Errorf("on %s: want %s, got %s", step.day, step.want, got)
		}
	}
}

// A status the office picked by hand stays put.
func TestAPinnedTournamentStatusIsLeftAlone(t *testing.T) {
	d := newDB(t)
	c := &client{t: t, srv: newServerOn(t, d)}
	c.login("admin@jca.ac.th")
	_, trn, _ := c.do("POST", "/api/v1/tournaments", map[string]any{
		"name": "Called off", "start_date": academyDay(5), "tournament_status": "Completed", "status_locked": true,
	})
	id := trn["tournament_id"].(string)
	if err := api.SyncTournamentStatus(d, academyDay(0)); err != nil {
		t.Fatal(err)
	}
	if got := tournamentStatusOf(t, c, id); got != "Completed" {
		t.Fatalf("pinned status changed to %s", got)
	}
	// Unpinning hands it back to the dates.
	c.do("PATCH", "/api/v1/tournaments/"+id, map[string]any{"status_locked": false})
	if got := tournamentStatusOf(t, c, id); got != "Upcoming" {
		t.Fatalf("unpinned: want Upcoming, got %s", got)
	}
}
