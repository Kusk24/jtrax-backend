// The fields the front desk types in and the console used to drop on the
// floor: the child's school, their FIDE ID, and a payment that has not cleared
// yet. Each one was collected by a form and had nowhere to go.
package api_test

import "testing"

func TestStudentKeepsSchoolAndFideID(t *testing.T) {
	c := &client{t: t, srv: newServer(t)}
	c.login("admin@jca.ac.th")

	status, created, _ := c.do("POST", "/api/v1/students", map[string]any{
		"name":           "Nid Chaiyaporn",
		"current_school": "Sathorn Primary",
		"fide_id":        "6301234",
	})
	if status != 201 {
		t.Fatalf("create: want 201, got %d (%v)", status, created)
	}
	if created["current_school"] != "Sathorn Primary" || created["fide_id"] != "6301234" {
		t.Fatalf("registration fields not stored: %v", created)
	}

	status, got, _ := c.do("GET", "/api/v1/students/"+created["student_id"].(string), nil)
	if status != 200 || got["current_school"] != "Sathorn Primary" {
		t.Fatalf("school did not survive the round trip: %d %v", status, got)
	}
}

func TestPaymentRecordsStatusAndReference(t *testing.T) {
	c := &client{t: t, srv: newServer(t)}
	c.login("admin@jca.ac.th")

	status, student, _ := c.do("POST", "/api/v1/students", map[string]any{"name": "Pim"})
	if status != 201 {
		t.Fatalf("create student: %d (%v)", status, student)
	}
	sid := student["student_id"].(string)

	// A bank transfer the office has been promised but not yet received.
	status, pending, _ := c.do("POST", "/api/v1/payments", map[string]any{
		"student_id":       sid,
		"amount":           12000,
		"final_amount":     12000,
		"payment_method":   "BankTransfer",
		"status":           "Pending",
		"payment_date":     "2026-08-21",
		"reference_number": "BT-20260821-014",
	})
	if status != 201 {
		t.Fatalf("create payment: want 201, got %d (%v)", status, pending)
	}
	if pending["status"] != "Pending" || pending["reference_number"] != "BT-20260821-014" {
		t.Fatalf("status and reference were discarded: %v", pending)
	}

	status, refunded, _ := c.do("PATCH", "/api/v1/payments/"+pending["payment_id"].(string),
		map[string]any{"status": "Refunded"})
	if status != 200 || refunded["status"] != "Refunded" {
		t.Fatalf("refund: %d (%v)", status, refunded)
	}

	status, bad, _ := c.do("PATCH", "/api/v1/payments/"+pending["payment_id"].(string),
		map[string]any{"status": "Maybe"})
	if status != 400 {
		t.Fatalf("enum validation: want 400, got %d (%v)", status, bad)
	}
}
