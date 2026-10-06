package api_test

import (
	"testing"

	"github.com/Kusk24/jtrax-backend/internal/academytime"
)

/*
Nobody under a year old is registered: a student or a tournament entry with

	such a date of birth is refused, by whatever screen sends it.
*/
func TestADateOfBirthMustBeAYearAgo(t *testing.T) {
	admin := &client{t: t, srv: newServer(t)}
	admin.login("admin@jca.ac.th")
	young := academytime.Now().AddDate(0, -6, 0).Format("2006-01-02")
	future := academytime.Now().AddDate(1, 0, 0).Format("2006-01-02")
	ok := academytime.Now().AddDate(-1, 0, 0).Format("2006-01-02")

	for _, dob := range []string{young, future} {
		status, obj, _ := admin.do("POST", "/api/v1/students", map[string]any{"name": "Baby", "date_of_birth": dob})
		if status != 400 || obj["error"] != "the date of birth must be at least a year ago" {
			t.Errorf("student born %s: %d %v", dob, status, obj)
		}
	}
	if status, obj, _ := admin.do("POST", "/api/v1/students", map[string]any{"name": "One", "date_of_birth": ok}); status != 201 {
		t.Errorf("student born exactly a year ago: %d %v", status, obj)
	}

	_, trn, _ := admin.do("POST", "/api/v1/tournaments", map[string]any{"name": "Rapid"})
	status, obj, _ := admin.do("POST", "/api/v1/tournament-registrations", map[string]any{
		"tournament_id": trn["tournament_id"], "participant_name": "Baby", "participant_date_of_birth": young, "status": "Approved",
	})
	if status != 400 || obj["error"] != "the date of birth must be at least a year ago" {
		t.Errorf("entry born %s: %d %v", young, status, obj)
	}
}
