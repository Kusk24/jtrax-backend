package api_test

/* A family's own data: the export holds theirs and nobody else's, and an
   erasure request reaches the office and is closed by an admin. */

import (
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
)

func TestAParentExportsTheirFamilyAndNoOneElses(t *testing.T) {
	srv := newServer(t)
	parent := &client{t: t, srv: srv}
	parent.login("sandy01234@gmail.com")
	status, out, _ := parent.do("GET", "/api/v1/me/export", nil)
	if status != 200 {
		t.Fatalf("export: %d (%v)", status, out)
	}
	raw, _ := json.Marshal(out)
	text := string(raw)
	for _, want := range []string{"sandy01234@gmail.com", "Penny", "Uri"} {
		if !strings.Contains(text, want) {
			t.Errorf("export is missing %q", want)
		}
	}
	for _, never := range []string{"password_hash", "pbkdf2", "token_hash", "pay_code_hash", "admin@jca.ac.th"} {
		if strings.Contains(text, never) {
			t.Errorf("export leaks %q", never)
		}
	}
}

func TestStaffAreToldToAskTheOffice(t *testing.T) {
	admin := &client{t: t, srv: newServer(t)}
	admin.login("admin@jca.ac.th")
	if status, _, _ := admin.do("GET", "/api/v1/me/export", nil); status != 403 {
		t.Fatalf("staff export: want 403, got %d", status)
	}
	anon := &client{t: t, srv: admin.srv}
	if status, _, _ := anon.do("GET", "/api/v1/me/export", nil); status != 401 {
		t.Fatalf("anonymous export: want 401, got %d", status)
	}
}

func TestAnErasureRequestReachesTheOfficeOnce(t *testing.T) {
	srv := newServer(t)
	parent := &client{t: t, srv: srv}
	parent.login("sandy01234@gmail.com")
	status, first, _ := parent.do("POST", "/api/v1/me/erasure-request", map[string]any{"note": "We have moved abroad."})
	if status != 201 {
		t.Fatalf("request: %d (%v)", status, first)
	}
	_, again, _ := parent.do("POST", "/api/v1/me/erasure-request", map[string]any{})
	if again["data_request_id"] != first["data_request_id"] {
		t.Errorf("asking twice opened a second request: %v vs %v", again, first)
	}
	if _, mine, _ := parent.do("GET", "/api/v1/me/erasure-request", nil); mine["status"] != "Open" {
		t.Errorf("parent's view: %v", mine)
	}

	student := &client{t: t, srv: srv}
	student.login("penny@jca.ac.th")
	if status, _, _ := student.do("POST", "/api/v1/me/erasure-request", map[string]any{}); status != 403 {
		t.Errorf("a student asking for the family: want 403, got %d", status)
	}
	if status, _, _ := parent.do("GET", "/api/v1/data-requests", nil); status != 403 {
		t.Errorf("a parent listing requests: want 403, got %d", status)
	}

	admin := &client{t: t, srv: srv}
	admin.login("admin@jca.ac.th")
	_, _, list := admin.do("GET", "/api/v1/data-requests", nil)
	if len(list) != 1 || list[0]["requested_by_name"] != "Sandy Jones" || list[0]["note"] != "We have moved abroad." {
		t.Fatalf("office's list: %v", list)
	}
	id := first["data_request_id"].(string)
	if status, _, _ := admin.do("POST", "/api/v1/data-requests/"+id+"/resolve", map[string]any{"status": "Declined"}); status != 400 {
		t.Errorf("declining without a reason: want 400, got %d", status)
	}
	if status, _, _ := admin.do("POST", "/api/v1/data-requests/"+id+"/resolve", map[string]any{"status": "Done"}); status != 204 {
		t.Errorf("resolve: got %d", status)
	}
	if status, _, _ := admin.do("POST", "/api/v1/data-requests/"+id+"/resolve", map[string]any{"status": "Done"}); status != 404 {
		t.Errorf("resolving twice: want 404, got %d", status)
	}
}

func TestDeletingTheGuardianCompletesTheirRequest(t *testing.T) {
	srv := newServer(t)
	parent := &client{t: t, srv: srv}
	parent.login("sandy01234@gmail.com")
	_, req, _ := parent.do("POST", "/api/v1/me/erasure-request", map[string]any{})
	admin := &client{t: t, srv: srv}
	admin.login("admin@jca.ac.th")
	if status, out, _ := admin.do("DELETE", "/api/v1/parents/par_sandy/cascade?children=delete", nil); status != 200 && status != 204 {
		t.Fatalf("delete family: %d (%v)", status, out)
	}
	_, _, list := admin.do("GET", "/api/v1/data-requests", nil)
	if len(list) != 1 || list[0]["data_request_id"] != req["data_request_id"] || list[0]["status"] != "Done" {
		t.Fatalf("after the delete: %v", list)
	}
	if list[0]["requested_by_name"] != "Sandy Jones" {
		t.Errorf("the record of who asked was lost: %v", list[0])
	}
}

func TestTheExportIncludesWhatTheFamilyAgreedTo(t *testing.T) {
	c := &client{t: t, srv: newServer(t)}
	c.login("sandy01234@gmail.com")
	if status, row, _ := c.do("POST", "/api/v1/tournaments/trn_wellington/entries", verifiedEntry(t, c, map[string]any{
		"student_id": "stu_penny", "privacy_notice": true,
	})); status != 201 {
		t.Fatalf("enter: %d (%v)", status, row)
	}
	_, out, _ := c.do("GET", "/api/v1/me/export", nil)
	consents, _ := out["consents"].([]any)
	if len(consents) != 1 || out["notice_version"] == nil {
		t.Fatalf("export consents: %v, version %v", out["consents"], out["notice_version"])
	}
	if consents[0].(map[string]any)["purpose"] != "privacy_notice" {
		t.Errorf("consent: %v", consents[0])
	}
}

/* Erasing a family whose child has a history — a Lichess link, games against
   the engine, an ID card read, and a tournament game and fee — used to fail
   on the first reference the delete did not know about. */
func TestErasingAFamilyWithAHistoryGoesThrough(t *testing.T) {
	srv := newServer(t)
	parent := &client{t: t, srv: srv}
	parent.login("sandy01234@gmail.com")
	status, entry, _ := parent.do("POST", "/api/v1/tournaments/trn_wellington/entries", verifiedEntry(t, parent, map[string]any{
		"student_id": "stu_penny", "privacy_notice": true,
	}))
	if status != 201 {
		t.Fatalf("enter: %d (%v)", status, entry)
	}
	regID := entry["tournament_registration_id"].(string)
	v, _ := serverDBs.Load(srv.URL)
	d := v.(*sql.DB)
	for _, q := range []string{
		`INSERT INTO id_card_check (id_card_check_id, tournament_id, student_id, date_of_birth) VALUES ('chk_hist', 'trn_wellington', 'stu_penny', '2018-02-14')`,
		`INSERT INTO student_lichess (student_id, username, lichess_id) VALUES ('stu_penny', 'penny', 'penny')`,
		`INSERT INTO lichess_rating (student_id, perf, rating) VALUES ('stu_penny', 'blitz', 1200)`,
		`INSERT INTO solo_game (solo_game_id, student_id, opponent) VALUES ('sg_1', 'stu_penny', 'stockfish')`,
		`INSERT INTO tournament_registration (tournament_registration_id, tournament_id, participant_name) VALUES ('r_opp', 'trn_wellington', 'Opponent')`,
		`INSERT INTO tournament_round (tournament_round_id, tournament_id, round_no, status) VALUES ('rd_h', 'trn_wellington', 1, 'Completed')`,
		`INSERT INTO tournament_pairing (tournament_pairing_id, tournament_round_id, board_no, white_registration_id, black_registration_id, result) VALUES ('p_h', 'rd_h', 1, '` + regID + `', 'r_opp', '1-0')`,
	} {
		if _, err := d.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}

	admin := &client{t: t, srv: srv}
	admin.login("admin@jca.ac.th")
	if status, out, _ := admin.do("DELETE", "/api/v1/parents/par_sandy/cascade?children=delete", nil); status != 200 && status != 204 {
		t.Fatalf("erase family: %d (%v)", status, out)
	}

	var name string
	var student sql.NullString
	if err := d.QueryRow(`SELECT participant_name, student_id FROM tournament_registration WHERE tournament_registration_id = ?`, regID).
		Scan(&name, &student); err != nil {
		t.Fatalf("the opponent's game lost its entry: %v", err)
	}
	if name != "Former player" || student.Valid {
		t.Errorf("kept entry still says who played: %q %v", name, student)
	}
	for _, q := range []string{
		`SELECT COUNT(*) FROM student WHERE student_id = 'stu_penny'`,
		`SELECT COUNT(*) FROM solo_game WHERE student_id = 'stu_penny'`,
		`SELECT COUNT(*) FROM student_lichess WHERE student_id = 'stu_penny'`,
		`SELECT COUNT(*) FROM id_card_check WHERE student_id = 'stu_penny'`,
	} {
		if n := count(t, d, q); n != 0 {
			t.Errorf("%s: %d left", q, n)
		}
	}
}
