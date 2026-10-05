package api_test

import "testing"

/* A payment is edited, never deleted — not even by an admin. */
func TestPaymentCannotBeDeleted(t *testing.T) {
	c := &client{t: t, srv: newServer(t)}
	c.login("admin@jca.ac.th")

	status, created, _ := c.do("POST", "/api/v1/payments", map[string]any{
		"student_id": "stu_penny", "amount": 1000, "final_amount": 1000,
		"payment_method": "Cash", "status": "Paid", "payment_date": "2026-10-05",
	})
	if status != 201 {
		t.Fatalf("create: status %d, %v", status, created)
	}
	id, _ := created["payment_id"].(string)

	status, obj, _ := c.do("DELETE", "/api/v1/payments/"+id, nil)
	if status != 405 || obj["error"] != "payments can't be deleted — edit the payment instead" {
		t.Errorf("delete: status %d, %v; want 405 with the reason", status, obj)
	}
	if status, _, _ := c.do("GET", "/api/v1/payments/"+id, nil); status != 200 {
		t.Errorf("the payment is gone after a refused delete: status %d", status)
	}
	/* Editing still works. */
	if status, obj, _ := c.do("PATCH", "/api/v1/payments/"+id, map[string]any{"reference_number": "R-1"}); status != 200 {
		t.Errorf("edit: status %d, %v", status, obj)
	}
}
