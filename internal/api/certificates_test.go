// Certificates by course: private and group hours of one course add up, other
// courses stay apart, 50 hours alone certifies nobody, and a course is
// certified once.
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
