package api_test

import "testing"

/*
Cancelling an entry's fee takes the place out of the tournament, into

	Released places — by the desk's Cancel or by cancelling the payment itself —
	and restoring it brings back both the entry and its unpaid fee.
*/
func TestACancelledFeeReleasesThePlace(t *testing.T) {
	admin := &client{t: t, srv: newServer(t)}
	admin.login("admin@jca.ac.th")
	_, trn, _ := admin.do("POST", "/api/v1/tournaments", map[string]any{"name": "Rapid"})
	tid := trn["tournament_id"]
	entry := func(name string) string {
		status, reg, _ := admin.do("POST", "/api/v1/tournament-registrations", map[string]any{
			"tournament_id": tid, "participant_name": name, "status": "Approved", "fee_charged": 300,
		})
		if status != 201 {
			t.Fatalf("entry %s: %d %v", name, status, reg)
		}
		return reg["tournament_registration_id"].(string)
	}
	state := func(regID string) (string, any) {
		_, row, _ := admin.do("GET", "/api/v1/tournament-registrations/"+regID, nil)
		return row["status"].(string), row["released_at"]
	}

	/* Cancelling the payment itself. */
	ann := entry("Ann")
	_, pay, _ := admin.do("POST", "/api/v1/payments", map[string]any{
		"tournament_registration_id": ann, "amount": 300, "final_amount": 300,
		"payment_method": "Cash", "status": "Pending", "payment_date": "2026-10-06",
	})
	payID := pay["payment_id"].(string)
	if status, obj, _ := admin.do("PATCH", "/api/v1/payments/"+payID, map[string]any{"status": "Cancelled"}); status != 200 {
		t.Fatalf("cancel payment: %d %v", status, obj)
	}
	if st, at := state(ann); st != "Withdrawn" || at == nil {
		t.Fatalf("after its fee was cancelled, Ann is %s (released %v)", st, at)
	}

	/* Restored: in again, and owing again. */
	admin.do("PATCH", "/api/v1/tournament-registrations/"+ann, map[string]any{"status": "Approved"})
	_, p, _ := admin.do("GET", "/api/v1/payments/"+payID, nil)
	if st, _ := state(ann); st != "Approved" || p["status"] != "Pending" {
		t.Fatalf("restored: entry %s, payment %v", st, p["status"])
	}

	/* The desk's Cancel, on an entry with no payment at all. */
	bea := entry("Bea")
	if status, obj, _ := admin.do("POST", "/api/v1/tournament-registrations/"+bea+"/release", nil); status != 200 {
		t.Fatalf("release: %d %v", status, obj)
	}
	if st, at := state(bea); st != "Withdrawn" || at == nil {
		t.Fatalf("after Cancel, Bea is %s (released %v)", st, at)
	}

	/* A paid entry is refunded first, not cancelled. */
	cat := entry("Cat")
	admin.do("POST", "/api/v1/tournament-registrations/"+cat+"/desk-payment", map[string]any{"payment_method": "Cash"})
	if status, _, _ := admin.do("POST", "/api/v1/tournament-registrations/"+cat+"/release", nil); status != 409 {
		t.Errorf("cancelling a paid entry: %d, want 409", status)
	}

	/* Staff only. */
	parent := &client{t: t, srv: admin.srv}
	parent.login("sandy01234@gmail.com")
	if status, _, _ := parent.do("POST", "/api/v1/tournament-registrations/"+ann+"/release", nil); status != 403 {
		t.Errorf("a parent releasing a place: %d, want 403", status)
	}
}
