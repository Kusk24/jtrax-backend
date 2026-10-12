// An announcement shows to parents only between its start and end, and they
// are told once, when it starts.
package api_test

import (
	"testing"
	"time"

	"github.com/Kusk24/jtrax-backend/internal/academytime"
	"github.com/Kusk24/jtrax-backend/internal/api"
	"github.com/Kusk24/jtrax-backend/internal/mail"
	"github.com/Kusk24/jtrax-backend/internal/notify"
)

func TestAnnouncementShowsInsideItsWindow(t *testing.T) {
	d := newDB(t)
	srv := newServerOn(t, d)
	admin := &client{t: t, srv: srv}
	admin.login("admin@jca.ac.th")
	at := func(h time.Duration) string { return academytime.Now().Add(h).Format("2006-01-02T15:04") }
	post := func(title, from, until string) (int, map[string]any) {
		status, obj, _ := admin.do("POST", "/api/v1/announcements", map[string]any{
			"title": title, "body": "…", "author_user_account_id": "usr_serene",
			"show_from": from, "show_until": until,
		})
		return status, obj
	}

	if status, obj := post("Showing", at(-time.Hour), at(24*time.Hour)); status != 201 {
		t.Fatalf("showing: %d %v", status, obj)
	}
	if status, _ := post("Ended", at(-48*time.Hour), at(-24*time.Hour)); status != 201 {
		t.Fatalf("ended: %d", status)
	}
	start := at(48 * time.Hour)
	if status, _ := post("Later", start, ""); status != 201 {
		t.Fatalf("later: %d", status)
	}
	if status, _ := post("Backwards", at(2*time.Hour), at(time.Hour)); status != 400 {
		t.Fatalf("an end before the start should be refused, got %d", status)
	}

	parent := &client{t: t, srv: srv}
	parent.login("sandy01234@gmail.com")
	_, _, list := parent.do("GET", "/api/v1/announcements", nil)
	seen := map[string]bool{}
	for _, a := range list {
		seen[a["title"].(string)] = true
	}
	if !seen["Showing"] || seen["Ended"] || seen["Later"] {
		t.Fatalf("parent should see only the one showing now, saw %v", seen)
	}
	// Told about the one showing; not yet about the later one, nor the ended one.
	if got := countType(inbox(t, srv, "sandy01234@gmail.com"), "announcement"); got != 1 {
		t.Fatalf("want 1 announcement notification now, got %d", got)
	}

	// When its start comes, the later one is told — once.
	svc := notify.New(d, &captureSender{}, mail.Config{})
	api.RunAnnouncementSchedule(d, svc, start)
	api.RunAnnouncementSchedule(d, svc, start)
	if got := countType(inbox(t, srv, "sandy01234@gmail.com"), "announcement"); got != 2 {
		t.Fatalf("want 2 announcement notifications after the start, got %d", got)
	}
}
