package api_test

/* Retention: housekeeping always runs; policy rules stay off until the
   academy sets a period, and then touch only what is past it. */

import (
	"database/sql"
	"testing"
	"time"

	"github.com/Kusk24/jtrax-backend/internal/api"
)

func count(t *testing.T, d *sql.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := d.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func exec(t *testing.T, d *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := d.Exec(query, args...); err != nil {
		t.Fatal(err)
	}
}

func TestHousekeepingRemovesOnlyWhatIsPast(t *testing.T) {
	d := newDB(t)
	now := time.Now().UTC()
	rfc := func(age time.Duration) string { return now.Add(-age).Format(time.RFC3339) }
	sqlt := func(age time.Duration) string { return now.Add(-age).Format("2006-01-02 15:04:05") }

	exec(t, d, `INSERT INTO auth_session (token_hash, user_account_id, created_at, expires_at) VALUES
		('live', 'usr_admin1', ?, ?), ('idle', 'usr_admin1', ?, ?), ('capped', 'usr_admin1', ?, ?)`,
		sqlt(time.Hour), rfc(-24*time.Hour),
		sqlt(20*24*time.Hour), rfc(time.Hour),
		sqlt(91*24*time.Hour), rfc(-24*time.Hour))
	exec(t, d, `INSERT INTO password_reset (token_hash, user_account_id, expires_at, used_at) VALUES
		('fresh', 'usr_admin1', ?, NULL), ('spent-recently', 'usr_admin1', ?, ?), ('spent-long-ago', 'usr_admin1', ?, ?)`,
		rfc(-time.Hour), rfc(-time.Hour), rfc(24*time.Hour), rfc(40*24*time.Hour), rfc(40*24*time.Hour))
	exec(t, d, `INSERT INTO id_card_check (id_card_check_id, tournament_id, date_of_birth, created_at) VALUES
		('new', 'trn_wellington', '2016-01-01', ?), ('stale', 'trn_wellington', '2016-01-01', ?)`,
		sqlt(time.Hour), sqlt(2*24*time.Hour))

	report, err := api.SweepRetention(d, now, api.RetentionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if got := count(t, d, `SELECT COUNT(*) FROM auth_session WHERE token_hash IN ('live','idle','capped')`); got != 1 {
		t.Errorf("sessions left: %d, want only the live one", got)
	}
	if count(t, d, `SELECT COUNT(*) FROM auth_session WHERE token_hash = 'live'`) != 1 {
		t.Error("a live session was removed")
	}
	if got := count(t, d, `SELECT COUNT(*) FROM password_reset`); got != 2 {
		t.Errorf("reset links left: %d, want the fresh and the recently spent", got)
	}
	if got := count(t, d, `SELECT COUNT(*) FROM id_card_check`); got != 1 {
		t.Errorf("ID card reads left: %d, want the new one", got)
	}
	if report["expired sessions"] != 2 {
		t.Errorf("report: %v", report)
	}
}

func TestPolicyRulesWaitForAPeriod(t *testing.T) {
	d := newDB(t)
	now := time.Now().UTC()
	exec(t, d, `UPDATE tournament SET start_date = '2026-01-10', end_date = '2026-01-10' WHERE tournament_id = 'trn_wellington'`)
	exec(t, d, `INSERT INTO tournament_registration
		(tournament_registration_id, tournament_id, participant_name, contact_email, contact_phone, medical_notes, participant_date_of_birth)
		VALUES ('pub', 'trn_wellington', 'Public Kid', 'kid@example.com', '0800000000', 'asthma', '2016-01-01')`)

	// Off: nothing touched, however old.
	if _, err := api.SweepRetention(d, now, api.RetentionConfig{}); err != nil {
		t.Fatal(err)
	}
	if count(t, d, `SELECT COUNT(*) FROM tournament_registration WHERE tournament_registration_id = 'pub' AND medical_notes = 'asthma' AND contact_email <> ''`) != 1 {
		t.Fatal("a policy rule ran without a period set")
	}

	// A period longer than the event's age: still kept.
	long := api.RetentionConfig{MedicalNotesDays: 3650, PublicEntrantDays: 3650}
	if _, err := api.SweepRetention(d, now, long); err != nil {
		t.Fatal(err)
	}
	if count(t, d, `SELECT COUNT(*) FROM tournament_registration WHERE tournament_registration_id = 'pub' AND medical_notes = 'asthma'`) != 1 {
		t.Fatal("data inside the period was blanked")
	}

	// Past it: health data and contact details go, the published name stays.
	short := api.RetentionConfig{MedicalNotesDays: 30, PublicEntrantDays: 30}
	if _, err := api.SweepRetention(d, now, short); err != nil {
		t.Fatal(err)
	}
	var name, email, notes string
	var dob sql.NullString
	d.QueryRow(`SELECT participant_name, contact_email, medical_notes, participant_date_of_birth
		FROM tournament_registration WHERE tournament_registration_id = 'pub'`).Scan(&name, &email, &notes, &dob)
	if name != "Public Kid" || email != "" || notes != "" || dob.Valid {
		t.Errorf("after the period: name=%q email=%q notes=%q dob=%v", name, email, notes, dob)
	}
}

func TestRetentionFromEnvNeverDeletesOnATypo(t *testing.T) {
	t.Setenv("JTRAX_RETAIN_MEDICAL_NOTES_DAYS", "30")
	t.Setenv("JTRAX_RETAIN_PUBLIC_ENTRANT_DAYS", "thirty")
	t.Setenv("JTRAX_RETAIN_NOTIFICATION_DAYS", "-5")
	cfg := api.RetentionFromEnv()
	if cfg.MedicalNotesDays != 30 || cfg.PublicEntrantDays != 0 || cfg.NotificationDays != 0 || cfg.LineMessageDays != 0 {
		t.Errorf("config: %+v", cfg)
	}
}
