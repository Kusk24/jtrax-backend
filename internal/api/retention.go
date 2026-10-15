// Retention — personal data is deleted once nothing needs it any more
// (PDPA s.37(3); see the vault's "PDPA gap audit - 2026-10-15").
//
// Two kinds of rule. Housekeeping rules run always: they remove credentials
// and scratch data that no purpose ever needs past a fixed point — an expired
// session, a spent reset link, an ID card read that was never used. Policy
// rules are about records the academy might still want — a past event's
// medical notes, a public entrant's phone number, old notifications, LINE
// chat history — so each is off until the academy chooses a period and sets
// its variable; the code takes no view on how long is long enough.
package api

import (
	"context"
	"database/sql"
	"log"
	"os"
	"strconv"
	"time"
)

// retentionEvery is how often the sweep runs. Every period is in days, so
// hourly is never noticeably late.
const retentionEvery = time.Hour

// Housekeeping periods: how long spent credentials are kept, for anyone
// investigating "I never got the email" — after that they are only a liability.
const (
	spentResetKeep  = 30 * 24 * time.Hour
	deadPushKeep    = 30 * 24 * time.Hour
	idCardCheckKeep = 24 * time.Hour
)

// RetentionConfig holds the policy periods in days; 0 keeps the data.
type RetentionConfig struct {
	// After the tournament's last day: medical notes on every entry.
	MedicalNotesDays int
	// After the tournament's last day: a public (non-member) entrant's contact
	// email and phone, date of birth, remarks and what their ID card said.
	// Name and results stay — they are published.
	PublicEntrantDays int
	// Notifications already read, and their delivery records.
	NotificationDays int
	// LINE messages, by when they were sent.
	LineMessageDays int
}

// RetentionFromEnv reads the policy periods. An unset, empty or malformed
// variable is 0, which keeps the data: a typo must never delete anything.
func RetentionFromEnv() RetentionConfig {
	days := func(name string) int {
		n, err := strconv.Atoi(os.Getenv(name))
		if err != nil || n < 0 {
			return 0
		}
		return n
	}
	return RetentionConfig{
		MedicalNotesDays:  days("JTRAX_RETAIN_MEDICAL_NOTES_DAYS"),
		PublicEntrantDays: days("JTRAX_RETAIN_PUBLIC_ENTRANT_DAYS"),
		NotificationDays:  days("JTRAX_RETAIN_NOTIFICATION_DAYS"),
		LineMessageDays:   days("JTRAX_RETAIN_LINE_MESSAGE_DAYS"),
	}
}

// RetentionReport counts what one sweep removed or blanked, by rule.
type RetentionReport map[string]int64

// StartRetention runs SweepRetention now and then hourly until ctx ends.
// Started by the server, not by NewHandler, so tests decide when it runs.
func StartRetention(ctx context.Context, d *sql.DB, cfg RetentionConfig) {
	go func() {
		ticker := time.NewTicker(retentionEvery)
		defer ticker.Stop()
		for {
			report, err := SweepRetention(d, time.Now().UTC(), cfg)
			if err != nil {
				log.Printf("retention: %v", err)
			}
			for rule, n := range report {
				if n > 0 {
					log.Printf("retention: %s: %d", rule, n)
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

// SweepRetention applies every rule as of `now`. Each rule is one statement,
// so a failure leaves the others' work done; the first error is returned.
func SweepRetention(d *sql.DB, now time.Time, cfg RetentionConfig) (RetentionReport, error) {
	stamp := func(age time.Duration) string { return now.Add(-age).Format(sqliteTimeLayout) }
	day := func(days int) string { return now.AddDate(0, 0, -days).Format("2006-01-02") }
	rules := []struct {
		name  string
		on    bool
		query string
		args  []any
	}{
		// Sessions past their idle deadline or the 90-day cap (internal/auth).
		{"expired sessions", true,
			`DELETE FROM auth_session WHERE datetime(expires_at) < datetime(?) OR datetime(created_at) < datetime(?)`,
			[]any{stamp(0), stamp(90 * 24 * time.Hour)}},
		{"spent reset links", true,
			`DELETE FROM password_reset WHERE (used_at IS NOT NULL OR datetime(expires_at) < datetime(?))
			   AND datetime(COALESCE(used_at, expires_at)) < datetime(?)`,
			[]any{stamp(0), stamp(spentResetKeep)}},
		// Already pruned when the next card is read (idcheck.go); this covers
		// the quiet weeks when nobody reads one.
		{"unused ID card reads", true,
			`DELETE FROM id_card_check WHERE datetime(created_at) < datetime(?)`,
			[]any{stamp(idCardCheckKeep)}},
		{"dead push subscriptions", true,
			`DELETE FROM push_subscription WHERE failed_at IS NOT NULL AND datetime(failed_at) < datetime(?)`,
			[]any{stamp(deadPushKeep)}},

		{"medical notes", cfg.MedicalNotesDays > 0,
			`UPDATE tournament_registration SET medical_notes = ''
			  WHERE medical_notes <> '' AND tournament_id IN
			        (SELECT tournament_id FROM tournament WHERE COALESCE(end_date, start_date) < ?)`,
			[]any{day(cfg.MedicalNotesDays)}},
		{"public entrant details", cfg.PublicEntrantDays > 0,
			`UPDATE tournament_registration
			    SET contact_email = '', contact_phone = '', participant_contact = NULL,
			        participant_date_of_birth = NULL, remarks = '',
			        ocr_name = NULL, ocr_date_of_birth = NULL, id_document_type = NULL
			  WHERE student_id IS NULL
			    AND (contact_email <> '' OR contact_phone <> '' OR participant_date_of_birth IS NOT NULL
			         OR remarks <> '' OR ocr_name IS NOT NULL)
			    AND tournament_id IN
			        (SELECT tournament_id FROM tournament WHERE COALESCE(end_date, start_date) < ?)`,
			[]any{day(cfg.PublicEntrantDays)}},
		{"old notification deliveries", cfg.NotificationDays > 0,
			`DELETE FROM notification_delivery WHERE notification_id IN
			        (SELECT notification_id FROM notification
			          WHERE read_at IS NOT NULL AND datetime(created_at) < datetime(?))`,
			[]any{stamp(time.Duration(cfg.NotificationDays) * 24 * time.Hour)}},
		{"old notifications", cfg.NotificationDays > 0,
			`DELETE FROM notification WHERE read_at IS NOT NULL AND datetime(created_at) < datetime(?)`,
			[]any{stamp(time.Duration(cfg.NotificationDays) * 24 * time.Hour)}},
		{"old LINE messages", cfg.LineMessageDays > 0,
			`DELETE FROM line_message WHERE datetime(sent_at) < datetime(?)`,
			[]any{stamp(time.Duration(cfg.LineMessageDays) * 24 * time.Hour)}},
	}
	report := RetentionReport{}
	var first error
	for _, r := range rules {
		if !r.on {
			continue
		}
		res, err := d.Exec(r.query, r.args...)
		if err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		n, _ := res.RowsAffected()
		report[r.name] = n
	}
	return report, first
}
