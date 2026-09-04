package api_test

import (
	"net/http/httptest"
	"testing"
)

// The seed links parent Sandy (usr_sandy) as mother of Penny (stu_penny) and
// Uri (stu_uri); Penny signs in as usr_penny. These tests drive the real HTTP
// flow: a write happens, and the right person — and only the right person —
// finds a notification in their inbox.

func inbox(t *testing.T, srv *httptest.Server, email string) []map[string]any {
	t.Helper()
	c := &client{t: t, srv: srv}
	c.login(email)
	_, obj, _ := c.do("GET", "/api/v1/notifications", nil)
	list := []map[string]any{}
	if raw, ok := obj["notifications"].([]any); ok {
		for _, r := range raw {
			if m, ok := r.(map[string]any); ok {
				list = append(list, m)
			}
		}
	}
	return list
}

func countType(list []map[string]any, typ string) int {
	n := 0
	for _, row := range list {
		if row["type"] == typ {
			n++
		}
	}
	return n
}

func TestCheckInNotifiesParentOnly(t *testing.T) {
	srv := newServer(t)

	// A teacher checks Penny in for a session she has no attendance row on yet.
	teacher := &client{t: t, srv: srv}
	teacher.login("serene@jca.ac.th")
	status, _, _ := teacher.do("POST", "/api/v1/attendance", map[string]any{
		"student_id": "stu_penny", "session_id": "ses_b3",
		"check_in_time": "2026-05-20T13:59:00",
	})
	if status != 201 {
		t.Fatalf("check-in create: status %d", status)
	}

	// Penny's mother is notified.
	if got := countType(inbox(t, srv, "sandy01234@gmail.com"), "check_in"); got != 1 {
		t.Fatalf("parent should have exactly one check_in notification, got %d", got)
	}
	// Penny herself is not — check-in is for the guardian.
	if got := countType(inbox(t, srv, "penny@jca.ac.th"), "check_in"); got != 0 {
		t.Fatalf("student should not receive the parent's check_in notification, got %d", got)
	}
}

func TestCheckInDedupesOnRepeatedWrite(t *testing.T) {
	srv := newServer(t)
	teacher := &client{t: t, srv: srv}
	teacher.login("serene@jca.ac.th")

	// Create, then patch the same row again — the parent should still see one.
	status, obj, _ := teacher.do("POST", "/api/v1/attendance", map[string]any{
		"student_id": "stu_penny", "session_id": "ses_b3", "check_in_time": "2026-05-20T13:59:00",
	})
	if status != 201 {
		t.Fatalf("create: %d", status)
	}
	attID, _ := obj["attendance_id"].(string)
	teacher.do("PATCH", "/api/v1/attendance/"+attID, map[string]any{
		"check_in_time": "2026-05-20T14:00:00",
	})

	if got := countType(inbox(t, srv, "sandy01234@gmail.com"), "check_in"); got != 1 {
		t.Fatalf("repeated writes should notify once, got %d", got)
	}
}

func TestAnnouncementFansOutToStudentsAndParents(t *testing.T) {
	srv := newServer(t)
	teacher := &client{t: t, srv: srv}
	teacher.login("serene@jca.ac.th")

	status, _, _ := teacher.do("POST", "/api/v1/announcements", map[string]any{
		"title": "Songkran break", "body": "No classes 13-15 April.",
		"author_user_account_id": "usr_serene",
	})
	if status != 201 {
		t.Fatalf("announcement create: status %d", status)
	}

	for _, email := range []string{"sandy01234@gmail.com", "penny@jca.ac.th"} {
		if got := countType(inbox(t, srv, email), "announcement"); got != 1 {
			t.Fatalf("%s should have the announcement, got %d", email, got)
		}
	}
}

func TestCreditExpiryIsStaffOnly(t *testing.T) {
	d := newDB(t)
	srv := newServerOn(t, d)

	// A parent may not fire the manual trigger.
	parent := &client{t: t, srv: srv}
	parent.login("sandy01234@gmail.com")
	if status, _, _ := parent.do("POST", "/api/v1/notifications/credit-expiry", nil); status != 403 {
		t.Fatalf("parent should be forbidden, got %d", status)
	}

	// Give Penny a credit lot expiring next week; the seed's lots are all past.
	if _, err := d.Exec(
		`INSERT INTO credit_transaction (credit_transaction_id, enrollment_id, transaction_type, amount, expiry_date, transaction_date)
		 VALUES ('ctx_soon','enr_penny','purchase',10, date('now','+7 days'), date('now'))`); err != nil {
		t.Fatal(err)
	}

	admin := &client{t: t, srv: srv}
	admin.login("admin@jca.ac.th")
	status, obj, _ := admin.do("POST", "/api/v1/notifications/credit-expiry?days=14", nil)
	if status != 200 {
		t.Fatalf("admin trigger: status %d", status)
	}
	if n, _ := obj["students_notified"].(float64); n < 1 {
		t.Fatalf("expected at least one student notified, got %v", obj["students_notified"])
	}
	if got := countType(inbox(t, srv, "sandy01234@gmail.com"), "credit_expiry"); got != 1 {
		t.Fatalf("parent should have a credit_expiry notification, got %d", got)
	}
}

func TestNotificationSettingsRoundTrip(t *testing.T) {
	srv := newServer(t)
	c := &client{t: t, srv: srv}
	c.login("sandy01234@gmail.com")

	// Turn off the email channel for check-ins.
	if status, _, _ := c.do("PUT", "/api/v1/notification-settings", map[string]any{
		"type": "check_in", "channel": "email", "enabled": false,
	}); status != 200 {
		t.Fatalf("put setting: status %d", status)
	}
	_, obj, _ := c.do("GET", "/api/v1/notification-settings", nil)
	found := false
	if raw, ok := obj["settings"].([]any); ok {
		for _, r := range raw {
			m, _ := r.(map[string]any)
			if m["type"] == "check_in" && m["channel"] == "email" && m["enabled"] == false {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("saved setting not reflected: %v", obj["settings"])
	}

	// The in-app inbox may never be switched off.
	if status, _, _ := c.do("PUT", "/api/v1/notification-settings", map[string]any{
		"type": "check_in", "channel": "inapp", "enabled": false,
	}); status != 400 {
		t.Fatalf("disabling in-app should be rejected, got %d", status)
	}
}
