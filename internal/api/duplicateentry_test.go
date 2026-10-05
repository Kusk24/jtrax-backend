package api_test

import "testing"

/* The desk adding the same child, or the same email, twice is told so in words. */
func TestADuplicateEntryIsNamed(t *testing.T) {
	admin := &client{t: t, srv: newServer(t)}
	admin.login("admin@jca.ac.th")
	_, trn, _ := admin.do("POST", "/api/v1/tournaments", map[string]any{"name": "Rapid"})
	entry := func(body map[string]any) (int, map[string]any) {
		body["tournament_id"] = trn["tournament_id"]
		body["status"] = "Approved"
		status, obj, _ := admin.do("POST", "/api/v1/tournament-registrations", body)
		return status, obj
	}

	if status, obj := entry(map[string]any{"student_id": "stu_penny", "participant_name": "Penny"}); status != 201 {
		t.Fatalf("first entry: %d %v", status, obj)
	}
	status, obj := entry(map[string]any{"student_id": "stu_penny", "participant_name": "Penny"})
	if status != 409 || obj["error"] != "this child already has an entry in this tournament" {
		t.Errorf("same child again: %d %v", status, obj)
	}

	if status, obj := entry(map[string]any{"participant_name": "Ann", "contact_email": "ann@x.th"}); status != 201 {
		t.Fatalf("walk-in entry: %d %v", status, obj)
	}
	/* Another child on the same email is a second entry; the same player is not. */
	if status, obj := entry(map[string]any{"participant_name": "Bea", "contact_email": "ann@x.th"}); status != 201 {
		t.Errorf("a second player on the same email: %d %v", status, obj)
	}
	status, obj = entry(map[string]any{"participant_name": "ANN ", "contact_email": "Ann@x.th"})
	if status != 409 || obj["error"] != "this player is already entered in this tournament" {
		t.Errorf("same player again: %d %v", status, obj)
	}
}
