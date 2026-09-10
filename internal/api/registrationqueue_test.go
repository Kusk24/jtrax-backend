package api_test

import (
	"testing"
)

/* The staff side: the queue, and the two decisions that empty it. */

func TestRegistrationQueueIsStaffOnly(t *testing.T) {
	pub, id := openEvent(t, nil)
	pub.do("POST", "/api/v1/public/tournaments/"+id+"/register", entry(nil))

	// No session at all.
	if status, _, _ := pub.do("GET", "/api/v1/tournaments/"+id+"/registrations", nil); status != 401 {
		t.Fatalf("anonymous queue read: want 401, got %d", status)
	}
	// A parent has a session, but no business seeing other people's contacts.
	parent := &client{t: t, srv: pub.srv}
	parent.login("sandy01234@gmail.com")
	if status, _, _ := parent.do("GET", "/api/v1/tournaments/"+id+"/registrations", nil); status != 403 {
		t.Fatalf("parent queue read: want 403, got %d", status)
	}
}

func TestRegistrationQueueShowsTheStudentMatch(t *testing.T) {
	pub, id := openEvent(t, map[string]any{"student_discount_pct": 25})

	// A discount claim now has to name a student the academy can find, so the
	// unverifiable claim never reaches the queue at all — it is refused at the
	// door rather than left for staff to catch.
	if status, _, _ := pub.do("POST", "/api/v1/public/tournaments/"+id+"/register",
		entry(map[string]any{"email": "nobody@example.com", "isStudent": true})); status != 400 {
		t.Fatalf("unverifiable claim: want 400, got %d", status)
	}

	// What does reach it: a verified student, and an ordinary outsider.
	pub.do("POST", "/api/v1/public/tournaments/"+id+"/register",
		entry(map[string]any{"email": "penny@jca.ac.th", "isStudent": true, "studentId": "stu_penny"}))
	pub.do("POST", "/api/v1/public/tournaments/"+id+"/register",
		entry(map[string]any{"email": "nobody@example.com"}))

	staff := &client{t: t, srv: pub.srv}
	staff.login("admin@jca.ac.th")
	status, out, _ := staff.do("GET", "/api/v1/tournaments/"+id+"/registrations", nil)
	if status != 200 {
		t.Fatalf("queue: %d (%v)", status, out)
	}
	list, _ := out["registrations"].([]any)
	if len(list) != 2 {
		t.Fatalf("want 2 entries, got %d", len(list))
	}
	type seen struct {
		claimed bool
		matched string
	}
	rows := map[string]seen{}
	for _, raw := range list {
		e := raw.(map[string]any)
		name, _ := e["matchedStudentName"].(string)
		rows[e["contactEmail"].(string)] = seen{e["claimedStudent"] == true, name}
	}
	// Staff still see which entry is a pupil's and which is not.
	if got := rows["penny@jca.ac.th"]; !got.claimed || got.matched != "Penny" {
		t.Fatalf("verified student: claimed=%v matched=%q", got.claimed, got.matched)
	}
	if got := rows["nobody@example.com"]; got.claimed || got.matched != "" {
		t.Fatalf("outsider: claimed=%v matched=%q", got.claimed, got.matched)
	}
}

func TestApprovalAdmitsAndChargesTheQuotedFee(t *testing.T) {
	pub, id := openEvent(t, map[string]any{"student_discount_pct": 20})
	_, reg, _ := pub.do("POST", "/api/v1/public/tournaments/"+id+"/register",
		entry(map[string]any{"isStudent": true, "studentId": "stu_penny"}))
	if reg["feeQuoted"] != float64(400) {
		t.Fatalf("setup: want a 400 quote, got %v", reg["feeQuoted"])
	}

	staff := &client{t: t, srv: pub.srv}
	staff.login("admin@jca.ac.th")
	regID := firstRegistrationID(t, staff, id)

	if status, out, _ := staff.do("POST", "/api/v1/tournaments/registrations/"+regID+"/approve", nil); status != 200 {
		t.Fatalf("approve: %d (%v)", status, out)
	}
	e := firstRegistration(t, staff, id)
	if e["status"] != "Approved" {
		t.Fatalf("want Approved, got %v", e["status"])
	}
	// The quote becomes the charge, so the tournament's revenue is real.
	if e["feeCharged"] != float64(400) {
		t.Fatalf("want 400 charged, got %v", e["feeCharged"])
	}
}

// Staff remain the authority on the price even when the student ID checked
// out — a pupil who has since left is still a real id — so they can correct
// the fee at the moment they approve.
func TestApprovalCanOverrideTheClaimedDiscount(t *testing.T) {
	pub, id := openEvent(t, map[string]any{"student_discount_pct": 20})
	pub.do("POST", "/api/v1/public/tournaments/"+id+"/register",
		entry(map[string]any{"isStudent": true, "studentId": "stu_penny"}))

	staff := &client{t: t, srv: pub.srv}
	staff.login("admin@jca.ac.th")
	regID := firstRegistrationID(t, staff, id)

	// The discount should not have applied: charge the full fee.
	if status, out, _ := staff.do("POST", "/api/v1/tournaments/registrations/"+regID+"/approve",
		map[string]any{"fee": 500}); status != 200 {
		t.Fatalf("approve with override: %d (%v)", status, out)
	}
	if e := firstRegistration(t, staff, id); e["feeCharged"] != float64(500) {
		t.Fatalf("want the override charged, got %v", e["feeCharged"])
	}
}

// Two people working the same queue must not silently undo each other.
func TestARegistrationCannotBeDecidedTwice(t *testing.T) {
	pub, id := openEvent(t, nil)
	pub.do("POST", "/api/v1/public/tournaments/"+id+"/register", entry(nil))

	staff := &client{t: t, srv: pub.srv}
	staff.login("admin@jca.ac.th")
	regID := firstRegistrationID(t, staff, id)

	if status, _, _ := staff.do("POST", "/api/v1/tournaments/registrations/"+regID+"/approve", nil); status != 200 {
		t.Fatalf("first approve should succeed")
	}
	if status, _, _ := staff.do("POST", "/api/v1/tournaments/registrations/"+regID+"/reject", nil); status != 409 {
		t.Fatalf("second decision: want 409, got %d", status)
	}
}

// Approving past the limit would let the desk walk through the capacity rule
// the public path enforces, and find out on the day.
func TestApprovalStopsAtCapacity(t *testing.T) {
	pub, id := openEvent(t, map[string]any{"max_participants": 1})
	pub.do("POST", "/api/v1/public/tournaments/"+id+"/register", entry(nil))

	staff := &client{t: t, srv: pub.srv}
	staff.login("admin@jca.ac.th")
	regID := firstRegistrationID(t, staff, id)
	staff.do("POST", "/api/v1/tournaments/registrations/"+regID+"/approve", nil)

	// A second entry added directly by staff, then approved past the limit.
	_, extra, _ := staff.do("POST", "/api/v1/tournament-registrations", map[string]any{
		"tournament_id": id, "participant_name": "Walk-in", "status": "Pending",
	})
	second := extra["tournament_registration_id"].(string)
	if status, _, _ := staff.do("POST", "/api/v1/tournaments/registrations/"+second+"/approve", nil); status != 409 {
		t.Fatalf("approving past capacity: want 409, got %d", status)
	}
}

func firstRegistration(t *testing.T, staff *client, tournamentID string) map[string]any {
	t.Helper()
	_, out, _ := staff.do("GET", "/api/v1/tournaments/"+tournamentID+"/registrations", nil)
	list, _ := out["registrations"].([]any)
	if len(list) == 0 {
		t.Fatalf("no registrations for %s", tournamentID)
	}
	return list[0].(map[string]any)
}

func firstRegistrationID(t *testing.T, staff *client, tournamentID string) string {
	t.Helper()
	return firstRegistration(t, staff, tournamentID)["id"].(string)
}
