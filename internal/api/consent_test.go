package api_test

/* What a family agreed to is recorded with the notice's version, and health
   data is refused without explicit consent. */

import (
	"database/sql"
	"testing"

	"github.com/Kusk24/jtrax-backend/internal/families"
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
	if got["privacy_notice"] != families.PrivacyNoticeVersion {
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

// Both registration forms spend a one-use ID check before saving consent.
// A failed consent write must leave the same form and ID check usable for a retry.
func TestRegistrationCanRetryAfterConsentWriteFails(t *testing.T) {
	for _, kind := range []string{"public", "parent"} {
		t.Run(kind, func(t *testing.T) {
			c, event := openEvent(t, nil)
			database, _ := serverDBs.Load(c.srv.URL)
			d := database.(*sql.DB)
			var submit func() (int, map[string]any)
			if kind == "public" {
				form := withCheck(t, c, event, "", "2015-06-01", "idCheck", entry(nil))
				submit = func() (int, map[string]any) { return register(t, c, event, form, false) }
			} else {
				c.login("sandy01234@gmail.com")
				form := withCheck(t, c, event, "stu_penny", "2015-06-01", "id_check", map[string]any{"student_id": "stu_penny", "nickname": "Pen", "accept_terms": true, "privacy_notice": true})
				submit = func() (int, map[string]any) {
					code, row, _ := c.do("POST", "/api/v1/tournaments/"+event+"/entries", form)
					return code, row
				}
			}
			if _, err := d.Exec(`CREATE TRIGGER fail_entry_consent BEFORE INSERT ON consent_record
     BEGIN SELECT RAISE(ABORT,'consent write unavailable'); END`); err != nil {
				t.Fatal(err)
			}
			if status, body := submit(); status != 500 || body["error"] != "could not register" {
				t.Fatalf("failed write: %d %v", status, body)
			}
			var entries int
			if err := d.QueryRow(`SELECT COUNT(*) FROM tournament_registration WHERE tournament_id = ?`, event).Scan(&entries); err != nil || entries != 0 {
				t.Fatalf("entries after rollback: %d, err %v", entries, err)
			}
			if _, err := d.Exec(`DROP TRIGGER fail_entry_consent`); err != nil {
				t.Fatal(err)
			}
			if status, body := submit(); status != 201 || body["status"] != "Approved" {
				t.Fatalf("retry: %d %v", status, body)
			}
		})
	}
}
