// When parents see an announcement.
//
// show_from and show_until are optional times on the academy's clock
// ("2026-10-12T09:00"). Parents read an announcement only inside that window,
// and are notified once, when it opens — right away for one with no start.
package api

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"regexp"
	"time"

	"github.com/Kusk24/jtrax-backend/internal/academytime"
	"github.com/Kusk24/jtrax-backend/internal/notify"
)

// windowLayout is how show_from and show_until are written.
const windowLayout = "2006-01-02T15:04"

var windowTime = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}$`)

// academyMinute is now on the academy's clock, in windowLayout.
func academyMinute() string { return academytime.Now().Format(windowLayout) }

// visibleNow is the SQL that keeps an announcement inside its window.
const visibleNow = `(COALESCE(show_from, '') = '' OR show_from <= ?)
	AND (COALESCE(show_until, '') = '' OR show_until > ?)`

// checkAnnouncementWindow: each end is a time or blank, and the end is after
// the start.
func checkAnnouncementWindow(row map[string]any) error {
	from, _ := row["show_from"].(string)
	until, _ := row["show_until"].(string)
	for _, v := range []string{from, until} {
		if v != "" && !windowTime.MatchString(v) {
			return errors.New("show_from and show_until must look like 2026-10-12T09:00")
		}
	}
	if from != "" && until != "" && until <= from {
		return errors.New("the end must be after the start")
	}
	return nil
}

// notifyAnnouncement tells the announcement's parents, once. It claims the
// announcement first, so the posting and the timer cannot both send it.
func notifyAnnouncement(d *sql.DB, svc *notify.Service, annID string) {
	res, err := d.Exec(`UPDATE announcement SET notified_at = datetime('now')
	                     WHERE announcement_id = ? AND notified_at IS NULL`, annID)
	if err != nil {
		log.Printf("announcement %s: %v", annID, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return
	}
	var title, body, audience string
	if err := d.QueryRow(`SELECT title, body, COALESCE(audience, 'all') FROM announcement WHERE announcement_id = ?`,
		annID).Scan(&title, &body, &audience); err != nil {
		log.Printf("announcement %s: %v", annID, err)
		return
	}
	recipients := announcementAccounts(d, annID, audience)
	if len(recipients) == 0 {
		return
	}
	svc.Send(recipients, notify.Message{
		Type:      notify.TypeAnnouncement,
		Title:     notify.Text{EN: title, TH: title},
		Body:      notify.Text{EN: body, TH: body},
		Data:      map[string]any{"announcementId": annID},
		DedupeKey: "announcement:" + annID,
	})
}

// RunAnnouncementSchedule notifies every scheduled announcement whose window
// has opened and not yet closed, and which has not been told. Exported for
// tests.
func RunAnnouncementSchedule(d *sql.DB, svc *notify.Service, now string) {
	// Only scheduled ones: one with no start was told when it was posted.
	rows, err := d.Query(`SELECT announcement_id FROM announcement
	                       WHERE notified_at IS NULL AND COALESCE(show_from, '') <> ''
	                         AND `+visibleNow, now, now)
	if err != nil {
		log.Printf("announcement schedule: %v", err)
		return
	}
	var due []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			due = append(due, id)
		}
	}
	rows.Close()
	for _, id := range due {
		notifyAnnouncement(d, svc, id)
	}
}

// RunAnnouncementScheduleFor notifies one announcement if it is showing now.
func RunAnnouncementScheduleFor(d *sql.DB, svc *notify.Service, annID, now string) {
	var due bool
	if err := d.QueryRow(`SELECT 1 FROM announcement
	                       WHERE announcement_id = ? AND notified_at IS NULL AND `+visibleNow,
		annID, now, now).Scan(&due); err != nil {
		return
	}
	notifyAnnouncement(d, svc, annID)
}

// StartAnnouncementSchedule runs RunAnnouncementSchedule every minute.
func StartAnnouncementSchedule(ctx context.Context, d *sql.DB, svc *notify.Service) {
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			RunAnnouncementSchedule(d, svc, academyMinute())
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}
