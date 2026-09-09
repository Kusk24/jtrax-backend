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

	// The in-app row is the type's master toggle, so switching it off is a
	// legitimate choice — it is exactly what the portal's per-alert toggles do.
	if status, _, _ := c.do("PUT", "/api/v1/notification-settings", map[string]any{
		"type": "check_in", "channel": "inapp", "enabled": false,
	}); status != 200 {
		t.Fatalf("turning a type off: got %d, want 200", status)
	}
}

// checkOut checks Penny in and out of the seeded scheduled session ses_b3
// (14:00–15:00, one hour), which charges one credit at check-out.
func checkOut(t *testing.T, srv *httptest.Server) {
	t.Helper()
	teacher := &client{t: t, srv: srv}
	teacher.login("serene@jca.ac.th")
	status, obj, _ := teacher.do("POST", "/api/v1/attendance", map[string]any{
		"student_id": "stu_penny", "session_id": "ses_b3", "check_in_time": "2026-05-20T13:59:00",
	})
	if status != 201 {
		t.Fatalf("check-in: %d", status)
	}
	attID, _ := obj["attendance_id"].(string)
	if status, _, _ := teacher.do("PATCH", "/api/v1/attendance/"+attID, map[string]any{
		"check_out_time": "2026-05-20T15:01:00",
	}); status != 200 {
		t.Fatalf("check-out: %d", status)
	}
}

func bodyOfType(list []map[string]any, typ string) string {
	for _, row := range list {
		if row["type"] == typ {
			b, _ := row["body"].(string)
			return b
		}
	}
	return ""
}

func TestCheckOutSendsTheCreditDeduction(t *testing.T) {
	srv := newServer(t)
	checkOut(t, srv)

	in := inbox(t, srv, "sandy01234@gmail.com")
	if got := countType(in, "credit_deducted"); got != 1 {
		t.Fatalf("credit_deducted notifications: %d, want 1", got)
	}
	body := bodyOfType(in, "credit_deducted")
	// The class ran 14:00–15:00 and cost one credit; Penny's seeded balance of
	// 19 drops to 18. The message is the receipt, so the numbers are the test.
	for _, want := range []string{"14:00 – 15:00", "Credit used: 1.", "Remaining credit: 18."} {
		if !contains(body, want) {
			t.Fatalf("deduction body missing %q: %s", want, body)
		}
	}
	// The plain checkout message is superseded, not doubled.
	if got := countType(in, "check_out"); got != 0 {
		t.Fatalf("plain check_out still sent alongside the deduction: %d", got)
	}
}

func TestLowCreditIsOptIn(t *testing.T) {
	srv := newServer(t)

	// The academy's line is set above Penny's balance, so her check-out
	// leaves her "low" by the rule the console shows.
	admin := &client{t: t, srv: srv}
	admin.login("admin@jca.ac.th")
	if status, _, _ := admin.do("POST", "/api/v1/system-configuration", map[string]any{
		"config_key": "credit_rule_low_credit", "config_value": "100",
	}); status != 201 {
		t.Fatalf("setting the low-credit rule failed")
	}

	checkOut(t, srv)
	// Nothing: low credit is off until the parent turns it on.
	if got := countType(inbox(t, srv, "sandy01234@gmail.com"), "low_credit"); got != 0 {
		t.Fatalf("low_credit sent without opt-in: %d", got)
	}

	// Sandy opts in; the next chargeable check-out (Uri's) nudges her.
	sandy := &client{t: t, srv: srv}
	sandy.login("sandy01234@gmail.com")
	if status, _, _ := sandy.do("PUT", "/api/v1/notification-settings", map[string]any{
		"type": "low_credit", "channel": "inapp", "enabled": true,
	}); status != 200 {
		t.Fatalf("opting in failed")
	}
	// Uri's seeded attendance is corrected, which recharges the class and
	// re-runs the deduction — the same write path Class History uses.
	teacher := &client{t: t, srv: srv}
	teacher.login("serene@jca.ac.th")
	if status, _, _ := teacher.do("PATCH", "/api/v1/attendance/att_3", map[string]any{
		"check_out_time": "2026-05-10T10:02:00",
	}); status != 200 {
		t.Fatalf("correcting Uri's attendance failed")
	}
	if got := countType(inbox(t, srv, "sandy01234@gmail.com"), "low_credit"); got != 1 {
		t.Fatalf("low_credit after opting in: %d, want 1", got)
	}
}

func TestPaymentPaidNotifiesTheGuardianOnce(t *testing.T) {
	srv := newServer(t)
	admin := &client{t: t, srv: srv}
	admin.login("admin@jca.ac.th")

	// The desk takes money and records it Paid outright.
	status, obj, _ := admin.do("POST", "/api/v1/payments", map[string]any{
		"student_id": "stu_penny", "enrollment_id": "enr_penny",
		"student_name": "Penny", "amount": 12000, "final_amount": 12000,
		"payment_method": "Cash", "status": "Paid", "payment_date": "2026-09-09",
	})
	if status != 201 {
		t.Fatalf("recording payment: %d", status)
	}
	payID, _ := obj["payment_id"].(string)

	in := inbox(t, srv, "sandy01234@gmail.com")
	if got := countType(in, "payment_received"); got != 1 {
		t.Fatalf("payment_received: %d, want 1", got)
	}
	if body := bodyOfType(in, "payment_received"); !contains(body, "12,000 THB") {
		t.Fatalf("receipt does not name the amount: %s", body)
	}

	// Editing the settled row later must not send a second receipt.
	admin.do("PATCH", "/api/v1/payments/"+payID, map[string]any{"reference_number": "R-1"})
	if got := countType(inbox(t, srv, "sandy01234@gmail.com"), "payment_received"); got != 1 {
		t.Fatalf("edit re-sent the receipt: %d", got)
	}
}

func TestAdminCanSwitchATypeOffForTheSchool(t *testing.T) {
	srv := newServer(t)
	admin := &client{t: t, srv: srv}
	admin.login("admin@jca.ac.th")
	if status, _, _ := admin.do("POST", "/api/v1/system-configuration", map[string]any{
		"config_key": "notify_check_in", "config_value": "off",
	}); status != 201 {
		t.Fatalf("switching check_in off failed")
	}

	teacher := &client{t: t, srv: srv}
	teacher.login("serene@jca.ac.th")
	teacher.do("POST", "/api/v1/attendance", map[string]any{
		"student_id": "stu_penny", "session_id": "ses_b3", "check_in_time": "2026-05-20T13:59:00",
	})
	if got := countType(inbox(t, srv, "sandy01234@gmail.com"), "check_in"); got != 0 {
		t.Fatalf("check_in sent while the school switch is off: %d", got)
	}
}

func contains(haystack, needle string) bool {
	return len(needle) == 0 || (len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0)
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
