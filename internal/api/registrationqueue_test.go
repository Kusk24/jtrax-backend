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

// Nothing is approved any more, so the thing approval used to do at the door
// has to happen at the door: an entry arrives admitted, and the fee it was
// quoted is the fee it is charged. A quote that never became a charge would
// leave every public entry owing nothing on the desk's own roster.
func TestAnEntryIsAdmittedAndChargedWithoutAnybodyDeciding(t *testing.T) {
	pub, id := openEvent(t, map[string]any{"student_discount_pct": 20})
	_, reg, _ := pub.do("POST", "/api/v1/public/tournaments/"+id+"/register",
		entry(map[string]any{"isStudent": true, "studentId": "stu_penny"}))
	if reg["feeQuoted"] != float64(400) {
		t.Fatalf("setup: want a 400 quote, got %v", reg["feeQuoted"])
	}

	staff := &client{t: t, srv: pub.srv}
	staff.login("admin@jca.ac.th")
	e := firstRegistration(t, staff, id)

	// No staff call in between — this is the state it landed in.
	if e["status"] != "Approved" {
		t.Fatalf("want Approved on arrival, got %v", e["status"])
	}
	if e["feeCharged"] != float64(400) {
		t.Fatalf("want 400 charged, got %v", e["feeCharged"])
	}
}

// The approve and reject endpoints are gone, not merely unused by the console.
// A build of the console left running against a new server must not be able to
// put a row back into a state nothing can resolve.
func TestTheApprovalEndpointsAreGone(t *testing.T) {
	pub, id := openEvent(t, nil)
	pub.do("POST", "/api/v1/public/tournaments/"+id+"/register", entry(nil))

	staff := &client{t: t, srv: pub.srv}
	staff.login("admin@jca.ac.th")
	regID := firstRegistrationID(t, staff, id)

	for _, verb := range []string{"approve", "reject"} {
		if status, _, _ := staff.do("POST", "/api/v1/tournaments/registrations/"+regID+"/"+verb, nil); status != 404 {
			t.Errorf("%s: want 404, got %d", verb, status)
		}
	}
}

// Staff remain the authority on the price — a pupil who has since left is
// still a real id — but they correct it on the row now rather than at a
// decision that no longer exists.
func TestStaffCanStillCorrectTheFee(t *testing.T) {
	pub, id := openEvent(t, map[string]any{"student_discount_pct": 20})
	pub.do("POST", "/api/v1/public/tournaments/"+id+"/register",
		entry(map[string]any{"isStudent": true, "studentId": "stu_penny"}))

	staff := &client{t: t, srv: pub.srv}
	staff.login("admin@jca.ac.th")
	regID := firstRegistrationID(t, staff, id)

	if status, out, _ := staff.do("PATCH", "/api/v1/tournament-registrations/"+regID,
		map[string]any{"fee_charged": 500}); status != 200 {
		t.Fatalf("correcting the fee: %d (%v)", status, out)
	}
	if got := firstRegistration(t, staff, id)["feeCharged"]; got != float64(500) {
		t.Fatalf("want 500 after correction, got %v", got)
	}
}

// And withdrawing is how somebody comes back out of a tournament, since
// rejecting them is no longer a thing that can happen.
func TestStaffCanWithdrawAnEntry(t *testing.T) {
	pub, id := openEvent(t, nil)
	pub.do("POST", "/api/v1/public/tournaments/"+id+"/register", entry(nil))

	staff := &client{t: t, srv: pub.srv}
	staff.login("admin@jca.ac.th")
	regID := firstRegistrationID(t, staff, id)

	if status, out, _ := staff.do("PATCH", "/api/v1/tournament-registrations/"+regID,
		map[string]any{"status": "Withdrawn"}); status != 200 {
		t.Fatalf("withdrawing: %d (%v)", status, out)
	}
	if got := firstRegistration(t, staff, id)["status"]; got != "Withdrawn" {
		t.Fatalf("want Withdrawn, got %v", got)
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
