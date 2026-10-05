package api_test

import (
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/Kusk24/jtrax-backend/internal/api"
	"github.com/Kusk24/jtrax-backend/internal/mail"
)

var arrivalCode = regexp.MustCompile(`/arrival/([^#\s]+)#code=([0-9a-f]{64})`)

func TestArrivalReminderIsSentOnceOnTheConfiguredDay(t *testing.T) {
	d := newDB(t)
	srv := newServerOn(t, d)
	admin := &client{t: t, srv: srv}
	admin.login("admin@jca.ac.th")

	start := academyDay(10)
	_, trn, _ := admin.do("POST", "/api/v1/tournaments", map[string]any{
		"name": "Wellington Rapid", "start_date": start, "arrival_reminder_days": 5,
	})
	status, reg, _ := admin.do("POST", "/api/v1/tournament-registrations", map[string]any{
		"tournament_id": trn["tournament_id"], "student_id": "stu_penny",
		"participant_name": "Penny", "status": "Approved",
	})
	if status != 201 {
		t.Fatalf("register: %d %v", status, reg)
	}
	regID := reg["tournament_registration_id"].(string)
	cap := &captureSender{}
	cfg := mail.Config{AppURL: "https://portal.example"}

	// Six days early: not yet.
	if n, err := api.SendArrivalReminders(d, cfg, cap, academyDay(4)); err != nil || n != 0 {
		t.Fatalf("too early: asked %d, %v", n, err)
	}
	// Five days before: Sandy, Penny's mother, is asked.
	if n, err := api.SendArrivalReminders(d, cfg, cap, academyDay(5)); err != nil || n != 1 {
		t.Fatalf("on the day: asked %d, %v", n, err)
	}
	if len(cap.sent) != 1 || cap.sent[0].To != "sandy01234@gmail.com" {
		t.Fatalf("want one email to Sandy, got %+v", cap.sent)
	}
	// And only once.
	if n, _ := api.SendArrivalReminders(d, cfg, cap, academyDay(6)); n != 0 {
		t.Fatalf("asked again: %d", n)
	}

	m := arrivalCode.FindStringSubmatch(cap.sent[0].Body)
	if m == nil || m[1] != regID {
		t.Fatalf("email has no confirm link for %s: %q", regID, cap.sent[0].Body)
	}
	code := m[2]

	public := &client{t: t, srv: srv}
	if status, _, _ := public.do("POST", "/api/v1/public/arrival/"+regID, map[string]any{"code": strings.Repeat("0", 64)}); status != 404 {
		t.Fatalf("wrong code: want 404, got %d", status)
	}
	status, entry, _ := public.do("POST", "/api/v1/public/arrival/"+regID, map[string]any{"code": code})
	if status != 200 || entry["status"] != "Pending" || entry["participantName"] != "Penny" {
		t.Fatalf("read: %d %v", status, entry)
	}
	status, entry, _ = public.do("POST", "/api/v1/public/arrival/"+regID+"/answer", map[string]any{"code": code, "answer": "NotAttending"})
	if status != 200 || entry["status"] != "NotAttending" {
		t.Fatalf("answer: %d %v", status, entry)
	}

	_, row, _ := admin.do("GET", "/api/v1/tournament-registrations/"+regID, nil)
	if row["arrival_status"] != "NotAttending" || row["arrival_answered_at"] == nil {
		t.Fatalf("the office should see the answer: %v", row)
	}
}

// Changing the schedule moves the reminders not yet sent.
func TestArrivalReminderFollowsTheNewSchedule(t *testing.T) {
	d := newDB(t)
	srv := newServerOn(t, d)
	admin := &client{t: t, srv: srv}
	admin.login("admin@jca.ac.th")

	_, trn, _ := admin.do("POST", "/api/v1/tournaments", map[string]any{
		"name": "Wellington Rapid", "start_date": academyDay(10), "arrival_reminder_days": 2,
	})
	admin.do("POST", "/api/v1/tournament-registrations", map[string]any{
		"tournament_id": trn["tournament_id"], "student_id": "stu_penny",
		"participant_name": "Penny", "status": "Approved",
	})
	cap := &captureSender{}
	cfg := mail.Config{AppURL: "https://portal.example"}

	if n, _ := api.SendArrivalReminders(d, cfg, cap, academyDay(3)); n != 0 {
		t.Fatalf("two days before, not seven: asked %d", n)
	}
	admin.do("PATCH", "/api/v1/tournaments/"+trn["tournament_id"].(string), map[string]any{"arrival_reminder_days": 7})
	if n, _ := api.SendArrivalReminders(d, cfg, cap, academyDay(3)); n != 1 {
		t.Fatalf("now seven days before: asked %d", n)
	}
}

func TestNoReminderWhenNoneIsSet(t *testing.T) {
	d := newDB(t)
	srv := newServerOn(t, d)
	admin := &client{t: t, srv: srv}
	admin.login("admin@jca.ac.th")
	_, trn, _ := admin.do("POST", "/api/v1/tournaments", map[string]any{"name": "Quiet Open", "start_date": academyDay(2)})
	admin.do("POST", "/api/v1/tournament-registrations", map[string]any{
		"tournament_id": trn["tournament_id"], "student_id": "stu_penny",
		"participant_name": "Penny", "status": "Approved",
	})
	if n, _ := api.SendArrivalReminders(d, mail.Config{}, &captureSender{}, academyDay(1)); n != 0 {
		t.Fatalf("no schedule, no reminder: asked %d", n)
	}
}

// The desk can send the reminder again to someone who has not answered.
func TestResendingTheArrivalReminder(t *testing.T) {
	d := newDB(t)
	cap := &captureSender{}
	srv := httptest.NewServer(api.NewHandlerWithMail(d, mail.Config{AppURL: "https://portal.example"}, cap))
	t.Cleanup(srv.Close)
	admin := &client{t: t, srv: srv}
	admin.login("admin@jca.ac.th")

	_, trn, _ := admin.do("POST", "/api/v1/tournaments", map[string]any{
		"name": "Wellington Rapid", "start_date": academyDay(10), "arrival_reminder_days": 5,
	})
	_, reg, _ := admin.do("POST", "/api/v1/tournament-registrations", map[string]any{
		"tournament_id": trn["tournament_id"], "student_id": "stu_penny",
		"participant_name": "Penny", "status": "Approved",
	})
	regID := reg["tournament_registration_id"].(string)
	resend := "/api/v1/tournament-registrations/" + regID + "/arrival-reminder"

	// Families cannot trigger it.
	parent := &client{t: t, srv: srv}
	parent.login("sandy01234@gmail.com")
	if status, _, _ := parent.do("POST", resend, nil); status != 403 {
		t.Fatalf("parent resend: want 403, got %d", status)
	}

	status, out, _ := admin.do("POST", resend, nil)
	if status != 200 || out["sent"] != float64(1) {
		t.Fatalf("resend: %d %v", status, out)
	}
	m := arrivalCode.FindStringSubmatch(cap.sent[len(cap.sent)-1].Body)
	if m == nil {
		t.Fatalf("no link in the resent email")
	}
	_, row, _ := admin.do("GET", "/api/v1/tournament-registrations/"+regID, nil)
	if row["arrival_reminded_at"] == nil {
		t.Fatalf("resend should mark the entry as asked: %v", row)
	}

	// Once they answer, there is nothing to chase.
	public := &client{t: t, srv: srv}
	public.do("POST", "/api/v1/public/arrival/"+regID+"/answer", map[string]any{"code": m[2], "answer": "Confirmed"})
	if status, _, _ := admin.do("POST", resend, nil); status != 409 {
		t.Fatalf("resend after an answer: want 409, got %d", status)
	}
}

// A parent with two children in one tournament gets one email naming both,
// whose one link answers for each child separately.
func TestArrivalReminderAsksAFamilyOnce(t *testing.T) {
	d := newDB(t)
	srv := newServerOn(t, d)
	admin := &client{t: t, srv: srv}
	admin.login("admin@jca.ac.th")
	_, trn, _ := admin.do("POST", "/api/v1/tournaments", map[string]any{
		"name": "Wellington Rapid", "start_date": academyDay(10), "arrival_reminder_days": 5,
	})
	ids := map[string]string{}
	for _, kid := range []struct{ id, name string }{{"stu_penny", "Penny"}, {"stu_uri", "Uri"}} {
		status, reg, _ := admin.do("POST", "/api/v1/tournament-registrations", map[string]any{
			"tournament_id": trn["tournament_id"], "student_id": kid.id, "participant_name": kid.name, "status": "Approved",
		})
		if status != 201 {
			t.Fatalf("register %s: %d %v", kid.name, status, reg)
		}
		ids[kid.name] = reg["tournament_registration_id"].(string)
	}

	cap := &captureSender{}
	if n, err := api.SendArrivalReminders(d, mail.Config{AppURL: "https://portal.example"}, cap, academyDay(5)); err != nil || n != 2 {
		t.Fatalf("asked %d entries, %v; want 2", n, err)
	}
	if len(cap.sent) != 1 || cap.sent[0].To != "sandy01234@gmail.com" {
		t.Fatalf("want one email to Sandy, got %d: %+v", len(cap.sent), cap.sent)
	}
	body := cap.sent[0].Body
	if !strings.Contains(body, "Penny and Uri") {
		t.Errorf("the email should name both children: %q", body)
	}

	/* The link: the first child's code, then each other child's after &also=. */
	m := regexp.MustCompile(`/arrival/([^#\s]+)#code=([0-9a-f]{64})&also=([^.\s]+)\.([0-9a-f]{64})`).FindStringSubmatch(body)
	if m == nil || m[1] != ids["Penny"] || m[3] != ids["Uri"] {
		t.Fatalf("no family link in %q", body)
	}
	public := &client{t: t, srv: srv}
	for _, a := range []struct{ id, code, answer string }{{m[1], m[2], "Confirmed"}, {m[3], m[4], "NotAttending"}} {
		if status, out, _ := public.do("POST", "/api/v1/public/arrival/"+a.id+"/answer",
			map[string]any{"code": a.code, "answer": a.answer}); status != 200 || out["status"] != a.answer {
			t.Fatalf("answer for %s: %d %v", a.id, status, out)
		}
	}
	_, penny, _ := admin.do("GET", "/api/v1/tournament-registrations/"+ids["Penny"], nil)
	_, uri, _ := admin.do("GET", "/api/v1/tournament-registrations/"+ids["Uri"], nil)
	if penny["arrival_status"] != "Confirmed" || uri["arrival_status"] != "NotAttending" {
		t.Errorf("each child keeps their own answer: Penny %v, Uri %v", penny["arrival_status"], uri["arrival_status"])
	}
}
