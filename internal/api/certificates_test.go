// Certificates by course: hours are the credits charged for a course's
// classes, private and group add up, other courses stay apart, 50 hours alone
// certifies nobody, and a course is certified once.
package api_test

import (
	"fmt"
	"testing"
)

func TestCertificatesByCourse(t *testing.T) {
	srv := newServer(t)
	admin := &client{t: t, srv: srv}
	admin.login("admin@jca.ac.th")

	class := func(name, typ, course string) string {
		body := map[string]any{"name": name, "class_type": typ}
		if course != "" {
			body["certificate_course"] = course
		}
		status, row, _ := admin.do("POST", "/api/v1/classes", body)
		if status != 201 {
			t.Fatalf("class %s: %d %v", name, status, row)
		}
		// Enrolled with credits, so each visit is charged — the charge is
		// what the hours are.
		_, enr, _ := admin.do("POST", "/api/v1/enrollments", map[string]any{
			"student_id": "stu_penny", "class_id": row["class_id"], "enrolled_date": "2026-06-01", "status": "Active",
		})
		admin.do("POST", "/api/v1/credit-transactions", map[string]any{
			"enrollment_id": enr["enrollment_id"], "transaction_type": "purchase",
			"amount": 100, "transaction_date": "2026-06-01",
		})
		return row["class_id"].(string)
	}
	private := class("JCA Starters", "Private", "starters")
	group := class("JCA Starters", "Group", "starters")
	juniors := class("JCA Juniors", "Group", "juniors")
	camp := class("Summer Camp", "Group", "")

	day := 0
	attend := func(classID, in, out string) string {
		day++
		date := fmt.Sprintf("2026-07-%02d", day)
		_, ses, _ := admin.do("POST", "/api/v1/class-sessions", map[string]any{
			"class_id": classID, "session_date": date, "start_time": "08:00", "end_time": "18:00",
			"session_status": "Completed",
		})
		status, row, _ := admin.do("POST", "/api/v1/attendance", map[string]any{
			"student_id": "stu_penny", "session_id": ses["session_id"],
			"check_in_time": date + "T" + in, "check_out_time": date + "T" + out,
		})
		if status != 201 {
			t.Fatalf("attendance: %d %v", status, row)
		}
		return row["attendance_id"].(string)
	}
	read := func(c *client) map[string]map[string]any {
		status, body, _ := c.do("GET", "/api/v1/students/stu_penny/certificates", nil)
		if status != 200 {
			t.Fatalf("certificates: %d %v", status, body)
		}
		out := map[string]map[string]any{}
		for _, x := range body["courses"].([]any) {
			m := x.(map[string]any)
			out[m["course"].(string)] = m
		}
		return out
	}
	certify := func(c *client, course string) int {
		status, _, _ := c.do("POST", "/api/v1/students/stu_penny/certificates", map[string]any{"course": course})
		return status
	}

	// 20 h private + 20 h group + 9 h group (an hour late) = 49 h of Starters.
	attend(private, "08:00:00", "18:00:00")
	attend(private, "08:00:00", "18:00:00")
	attend(group, "08:00:00", "18:00:00")
	attend(group, "08:00:00", "18:00:00")
	late := attend(group, "09:00:00", "18:00:00")
	attend(juniors, "08:00:00", "13:00:00")
	attend(camp, "08:00:00", "18:00:00")

	got := read(admin)
	if got["starters"]["hours"] != float64(49) || got["juniors"]["hours"] != float64(5) {
		t.Fatalf("hours by course = starters %v, juniors %v; want 49 and 5", got["starters"]["hours"], got["juniors"]["hours"])
	}
	if _, ok := got["nxt"]; ok {
		t.Fatal("a course with nothing in it should not be listed")
	}
	if certify(admin, "starters") != 409 {
		t.Fatal("49 hours should not be enough")
	}

	// The late check-in corrected: the hours follow, and 50 still certifies nobody.
	if status, body, _ := admin.do("PATCH", "/api/v1/attendance/"+late, map[string]any{
		"check_in_time": "2026-07-05T08:00:00",
	}); status != 200 {
		t.Fatalf("correct check-in: %d %v", status, body)
	}
	got = read(admin)
	if got["starters"]["hours"] != float64(50) || got["starters"]["status"] != "in_progress" {
		t.Fatalf("after the correction: %v", got["starters"])
	}

	// A parent reads the same, but cannot certify.
	parent := &client{t: t, srv: srv}
	parent.login("sandy01234@gmail.com")
	if read(parent)["starters"]["hours"] != float64(50) {
		t.Fatal("the parent should see the same hours")
	}
	if certify(parent, "starters") != 403 {
		t.Fatal("a parent cannot certify")
	}

	if certify(admin, "starters") != 201 {
		t.Fatal("staff should certify at 50 hours")
	}
	if certify(admin, "starters") != 409 {
		t.Fatal("a second certificate for the same course should be refused")
	}
	got = read(parent)
	if got["starters"]["status"] != "certified" || got["starters"]["certificate_id"] == nil {
		t.Fatalf("the parent should see the certificate: %v", got["starters"])
	}
	if got["juniors"]["status"] != "in_progress" {
		t.Fatal("other courses are unaffected")
	}

	// Another family's child is not theirs to read.
	_, other, _ := admin.do("POST", "/api/v1/students", map[string]any{"name": "Someone Else"})
	if status, _, _ := parent.do("GET", "/api/v1/students/"+other["student_id"].(string)+"/certificates", nil); status != 404 {
		t.Fatalf("another family's child: want 404, got %d", status)
	}
}

// Only class charges are hours: a visit to a course's class the child is not
// enrolled in charges nothing and adds nothing, and a desk adjustment to the
// credits is not time in class.
func TestCertificateHoursAreClassChargesOnly(t *testing.T) {
	srv := newServer(t)
	admin := &client{t: t, srv: srv}
	admin.login("admin@jca.ac.th")

	_, cls, _ := admin.do("POST", "/api/v1/classes", map[string]any{
		"name": "JCA Starters", "class_type": "Group", "certificate_course": "starters",
	})
	_, other, _ := admin.do("POST", "/api/v1/classes", map[string]any{
		"name": "JCA Starters", "class_type": "Private", "certificate_course": "starters",
	})
	_, enr, _ := admin.do("POST", "/api/v1/enrollments", map[string]any{
		"student_id": "stu_penny", "class_id": cls["class_id"], "enrolled_date": "2026-06-01", "status": "Active",
	})
	admin.do("POST", "/api/v1/credit-transactions", map[string]any{
		"enrollment_id": enr["enrollment_id"], "transaction_type": "purchase", "amount": 20, "transaction_date": "2026-06-01",
	})
	admin.do("POST", "/api/v1/credit-transactions", map[string]any{
		"enrollment_id": enr["enrollment_id"], "transaction_type": "manual_adjustment", "amount": -5, "transaction_date": "2026-06-02",
	})
	attend := func(classID any, date, out string) {
		_, ses, _ := admin.do("POST", "/api/v1/class-sessions", map[string]any{
			"class_id": classID, "session_date": date, "start_time": "08:00", "end_time": "18:00", "session_status": "Completed",
		})
		if status, row, _ := admin.do("POST", "/api/v1/attendance", map[string]any{
			"student_id": "stu_penny", "session_id": ses["session_id"],
			"check_in_time": date + "T08:00:00", "check_out_time": date + "T" + out,
		}); status != 201 {
			t.Fatalf("attendance: %d %v", status, row)
		}
	}
	attend(cls["class_id"], "2026-07-01", "10:15:00")   // charged 2.25
	attend(other["class_id"], "2026-07-02", "12:00:00") // not enrolled: charged nothing

	status, body, _ := admin.do("GET", "/api/v1/students/stu_penny/certificates", nil)
	if status != 200 {
		t.Fatalf("certificates: %d %v", status, body)
	}
	for _, x := range body["courses"].([]any) {
		if m := x.(map[string]any); m["course"] == "starters" {
			if m["hours"] != 2.25 {
				t.Fatalf("starters hours = %v, want 2.25 (the one class charge)", m["hours"])
			}
			return
		}
	}
	t.Fatal("no starters course")
}

// A course deleted from the child is not listed — they are not working toward
// it — but its hours are not lost: the charges are detached, not removed, and
// count again when the child is back in the course.
func TestADeletedCourseKeepsItsHours(t *testing.T) {
	srv := newServer(t)
	admin := &client{t: t, srv: srv}
	admin.login("admin@jca.ac.th")

	_, cls, _ := admin.do("POST", "/api/v1/classes", map[string]any{
		"name": "JCA Starters", "class_type": "Group", "certificate_course": "starters",
	})
	enrol := func() string {
		_, e, _ := admin.do("POST", "/api/v1/enrollments", map[string]any{
			"student_id": "stu_penny", "class_id": cls["class_id"], "enrolled_date": "2026-06-01", "status": "Active",
		})
		admin.do("POST", "/api/v1/credit-transactions", map[string]any{
			"enrollment_id": e["enrollment_id"], "transaction_type": "purchase", "amount": 20, "transaction_date": "2026-06-01",
		})
		return e["enrollment_id"].(string)
	}
	first := enrol()
	_, ses, _ := admin.do("POST", "/api/v1/class-sessions", map[string]any{
		"class_id": cls["class_id"], "session_date": "2026-07-01", "start_time": "08:00", "end_time": "18:00", "session_status": "Completed",
	})
	admin.do("POST", "/api/v1/attendance", map[string]any{
		"student_id": "stu_penny", "session_id": ses["session_id"],
		"check_in_time": "2026-07-01T08:00:00", "check_out_time": "2026-07-01T10:00:00",
	})

	starters := func() map[string]any {
		_, body, _ := admin.do("GET", "/api/v1/students/stu_penny/certificates", nil)
		for _, x := range body["courses"].([]any) {
			if m := x.(map[string]any); m["course"] == "starters" {
				return m
			}
		}
		return nil
	}
	if got := starters(); got == nil || got["hours"] != float64(2) {
		t.Fatalf("enrolled: %v, want 2 hours", got)
	}

	// Deleted from Penny the way the console does it: entries detached,
	// the enrolment withdrawn and dated.
	_, _, txs := admin.do("GET", "/api/v1/credit-transactions?enrollment_id="+first, nil)
	if len(txs) < 2 {
		t.Fatalf("want the purchase and the class charge to detach, found %d", len(txs))
	}
	for _, tx := range txs {
		admin.do("PATCH", "/api/v1/credit-transactions/"+tx["credit_transaction_id"].(string), map[string]any{
			"enrollment_id": nil, "student_id": "stu_penny", "class_id": cls["class_id"],
		})
	}
	admin.do("PATCH", "/api/v1/enrollments/"+first, map[string]any{"status": "Withdrawn", "deleted_date": "2026-07-02"})
	if got := starters(); got != nil {
		t.Fatalf("a deleted course should not be listed: %v", got)
	}

	// Back in the course: the two hours are still hers.
	enrol()
	if got := starters(); got == nil || got["hours"] != float64(2) {
		t.Fatalf("back in the course: %v, want the 2 hours kept", got)
	}
}
