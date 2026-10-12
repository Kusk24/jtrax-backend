// A payment is edited with a history, or voided by an admin with a reason;
// voiding takes back what it bought and is never a delete.
package api_test

import (
	"testing"
)

func TestEditAndVoidAPayment(t *testing.T) {
	srv := newServer(t)
	admin := &client{t: t, srv: srv}
	admin.login("admin@jca.ac.th")
	admin.do("POST", "/api/v1/user-accounts", map[string]string{
		"email": "desk@jca.ac.th", "password": "chess1234", "role": "Receptionist", "display_name": "Front Desk",
	})
	desk := loginAs(t, admin, "desk@jca.ac.th", "chess1234")

	// A new course, bought: the enrolment, the payment, and its 10 credits.
	_, cls, _ := admin.do("POST", "/api/v1/classes", map[string]any{"name": "JCA Juniors", "class_type": "Group"})
	_, stu, _ := admin.do("POST", "/api/v1/students", map[string]any{"name": "Mini"})
	_, enr, _ := admin.do("POST", "/api/v1/enrollments", map[string]any{
		"student_id": stu["student_id"], "class_id": cls["class_id"], "enrolled_date": "2026-10-01", "status": "Active",
	})
	status, pay, _ := admin.do("POST", "/api/v1/payments", map[string]any{
		"student_id": stu["student_id"], "enrollment_id": enr["enrollment_id"], "credit_amount": 10,
		"amount": 5000, "final_amount": 5000, "payment_method": "Cash", "status": "Paid", "payment_date": "2026-10-01",
	})
	if status != 201 {
		t.Fatalf("payment: %d %v", status, pay)
	}
	payID := pay["payment_id"].(string)
	admin.do("POST", "/api/v1/credit-transactions", map[string]any{
		"enrollment_id": enr["enrollment_id"], "student_id": stu["student_id"], "class_id": cls["class_id"],
		"transaction_type": "purchase", "amount": 10, "transaction_date": "2026-10-01", "payment_id": payID,
	})

	// An edit is kept in the history: what changed, from and to, and who.
	if status, body, _ := desk.do("PATCH", "/api/v1/payments/"+payID, map[string]any{"final_amount": 4500}); status != 200 {
		t.Fatalf("edit: %d %v", status, body)
	}
	_, _, hist := admin.do("GET", "/api/v1/payments/"+payID+"/history", nil)
	if len(hist) != 1 || hist[0]["action"] != "edit" || hist[0]["by"] != "Front Desk" {
		t.Fatalf("history after the edit: %v", hist)
	}
	change := hist[0]["changes"].(map[string]any)["final_amount"].(map[string]any)
	if change["from"] != float64(5000) || change["to"] != float64(4500) {
		t.Fatalf("the edit should record 5000 → 4500: %v", change)
	}

	// Voiding: an admin's act, with a reason; "other" needs words.
	void := "/api/v1/payments/" + payID + "/void"
	if status, _, _ := desk.do("POST", void, map[string]any{"reason": "duplicate"}); status != 403 {
		t.Fatalf("a receptionist voiding: want 403, got %d", status)
	}
	if status, _, _ := admin.do("POST", void, map[string]any{}); status != 400 {
		t.Fatalf("no reason: want 400, got %d", status)
	}
	if status, _, _ := admin.do("POST", void, map[string]any{"reason": "other", "note": "  "}); status != 400 {
		t.Fatalf("other with no words: want 400, got %d", status)
	}
	if status, _, _ := admin.do("PATCH", "/api/v1/payments/"+payID, map[string]any{"status": "Voided"}); status != 400 {
		t.Fatalf("voiding by an edit: want 400, got %d", status)
	}
	status, out, _ := admin.do("POST", void, map[string]any{"reason": "other", "note": "Entered for the wrong family"})
	if status != 200 || out["credits_reversed"] != float64(-10) {
		t.Fatalf("void: %d %v", status, out)
	}

	// Still there, Voided, with its original amount; never deleted.
	_, row, _ := admin.do("GET", "/api/v1/payments/"+payID, nil)
	if row["status"] != "Voided" || row["final_amount"] != float64(4500) || row["void_reason"] != "other: Entered for the wrong family" {
		t.Fatalf("voided row: %v", row)
	}
	// Its credits are taken back by an adjustment pointing at it.
	_, _, txs := admin.do("GET", "/api/v1/credit-transactions?enrollment_id="+enr["enrollment_id"].(string), nil)
	sum := 0.0
	for _, tx := range txs {
		sum += tx["amount"].(float64)
	}
	if len(txs) != 2 || sum != 0 {
		t.Fatalf("credits after the void: %d rows summing %v", len(txs), sum)
	}
	// The enrolment existed only because of it, so it is ended.
	_, e, _ := admin.do("GET", "/api/v1/enrollments/"+enr["enrollment_id"].(string), nil)
	if e["status"] != "Withdrawn" {
		t.Fatalf("the enrolment opened by the voided payment should end: %v", e["status"])
	}

	// Done once, and closed to edits.
	if status, _, _ := admin.do("POST", void, map[string]any{"reason": "duplicate"}); status != 409 {
		t.Fatalf("voiding twice: want 409, got %d", status)
	}
	if status, _, _ := admin.do("PATCH", "/api/v1/payments/"+payID, map[string]any{"reference_number": "X"}); status != 409 {
		t.Fatalf("editing a voided payment: want 409, got %d", status)
	}
	if status, _, _ := admin.do("DELETE", "/api/v1/payments/"+payID, nil); status != 405 {
		t.Fatalf("deleting: want 405, got %d", status)
	}
	_, _, hist = admin.do("GET", "/api/v1/payments/"+payID+"/history", nil)
	if len(hist) != 2 || hist[1]["action"] != "void" || hist[1]["reason"] != "other" {
		t.Fatalf("history after the void: %v", hist)
	}
}

// An enrolment with classes behind it outlives a voided payment.
func TestVoidKeepsAnEnrolmentWithHistory(t *testing.T) {
	admin := &client{t: t, srv: newServer(t)}
	admin.login("admin@jca.ac.th")
	status, pay, _ := admin.do("POST", "/api/v1/payments", map[string]any{
		"student_id": "stu_penny", "enrollment_id": "enr_penny", "amount": 100, "final_amount": 100,
		"payment_method": "Cash", "status": "Paid", "payment_date": "2026-10-01",
	})
	if status != 201 {
		t.Fatalf("payment: %d %v", status, pay)
	}
	if status, body, _ := admin.do("POST", "/api/v1/payments/"+pay["payment_id"].(string)+"/void",
		map[string]any{"reason": "incorrect_amount"}); status != 200 {
		t.Fatalf("void: %d %v", status, body)
	}
	_, e, _ := admin.do("GET", "/api/v1/enrollments/enr_penny", nil)
	if e["status"] != "Active" {
		t.Fatalf("Penny's enrolment has classes behind it and should stay: %v", e["status"])
	}
}
