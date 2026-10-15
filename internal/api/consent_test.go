package api_test

/* What a family agreed to is recorded with the notice's version, and health
   data is refused without explicit consent. */

import (
	"database/sql"
	"testing"

	"github.com/Kusk24/jtrax-backend/internal/api"
)

func consents(t *testing.T, d *sql.DB, regID string) map[string]string {
	t.Helper()
	rows, err := d.Query(`SELECT purpose, notice_version FROM consent_record WHERE tournament_registration_id = ?`, regID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string]string{}
	for rows.Next() {
		var purpose, version string
		rows.Scan(&purpose, &version)
		got[purpose] = version
	}
	return got
}

func TestAPublicEntryNeedsThePrivacyNoticeAndRecordsIt(t *testing.T) {
	pub, id := openEvent(t, nil)
	body := entry(map[string]any{"nickname": "Chai", "age": 11})
	delete(body, "privacyNotice")
	if status, out := register(t, pub, id, body, true); status != 400 {
		t.Fatalf("without the privacy notice: want 400, got %d (%v)", status, out)
	}
	status, out := register(t, pub, id, entry(map[string]any{"nickname": "Chai", "age": 11}), true)
	if status != 201 {
		t.Fatalf("register: %d (%v)", status, out)
	}
	d, _ := serverDBs.Load(pub.srv.URL)
	got := consents(t, d.(*sql.DB), out["registrationId"].(string))
	if got["privacy_notice"] != api.PrivacyNoticeVersion {
		t.Errorf("consent records: %v", got)
	}
}

func TestMedicalNotesNeedExplicitConsent(t *testing.T) {
	c := &client{t: t, srv: newServer(t)}
	c.login("sandy01234@gmail.com")
	status, _, _ := c.do("POST", "/api/v1/tournaments/trn_wellington/entries", verifiedEntry(t, c, map[string]any{
		"student_id": "stu_penny", "medical_notes": "Asthma",
	}))
	if status != 400 {
		t.Fatalf("medical notes without consent: want 400, got %d", status)
	}
	status, row, _ := c.do("POST", "/api/v1/tournaments/trn_wellington/entries", verifiedEntry(t, c, map[string]any{
		"student_id": "stu_penny", "medical_notes": "Asthma", "health_consent": true, "privacy_notice": true,
	}))
	if status != 201 {
		t.Fatalf("with consent: %d (%v)", status, row)
	}
	d, _ := serverDBs.Load(c.srv.URL)
	got := consents(t, d.(*sql.DB), row["tournament_registration_id"].(string))
	if got["privacy_notice"] == "" || got["health_data"] == "" {
		t.Errorf("consent records: %v", got)
	}
}

func TestAnOlderAppStillEntersWithoutThePrivacyField(t *testing.T) {
	c := &client{t: t, srv: newServer(t)}
	c.login("sandy01234@gmail.com")
	status, row, _ := c.do("POST", "/api/v1/tournaments/trn_wellington/entries", verifiedEntry(t, c, map[string]any{
		"student_id": "stu_penny",
	}))
	if status != 201 {
		t.Fatalf("older build: %d (%v)", status, row)
	}
	d, _ := serverDBs.Load(c.srv.URL)
	if got := consents(t, d.(*sql.DB), row["tournament_registration_id"].(string)); len(got) != 0 {
		t.Errorf("recorded a notice nobody was shown: %v", got)
	}
}

func TestDeletingAnEntryOrAccountIsNotBlockedByItsConsent(t *testing.T) {
	c := &client{t: t, srv: newServer(t)}
	c.login("sandy01234@gmail.com")
	status, row, _ := c.do("POST", "/api/v1/tournaments/trn_wellington/entries", verifiedEntry(t, c, map[string]any{
		"student_id": "stu_penny", "privacy_notice": true,
	}))
	if status != 201 {
		t.Fatalf("enter: %d (%v)", status, row)
	}
	regID := row["tournament_registration_id"].(string)
	admin := &client{t: t, srv: c.srv}
	admin.login("admin@jca.ac.th")
	if s, out, _ := admin.do("DELETE", "/api/v1/tournament-registrations/"+regID, nil); s != 204 && s != 200 {
		t.Fatalf("delete entry: %d (%v)", s, out)
	}
	d, _ := serverDBs.Load(c.srv.URL)
	if got := consents(t, d.(*sql.DB), regID); len(got) != 0 {
		t.Errorf("consent outlived its entry: %v", got)
	}
}
