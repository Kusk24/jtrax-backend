package api_test

import (
	"testing"
)

// The seed gives Sandy two children: Penny (attendance, credits, a payment, an
// enrolment) and Uri. That is exactly the shape the office could not delete
// through the console — the plain DELETE refused every one of those references.

func TestPlainDeleteStillRefusesAReferencedStudent(t *testing.T) {
	c := &client{t: t, srv: newServer(t)}
	c.login("admin@jca.ac.th")

	status, _, _ := c.do("DELETE", "/api/v1/students/stu_penny", nil)
	if status != 409 {
		t.Fatalf("plain delete of a referenced student: want 409, got %d", status)
	}
	// And she is still there, rather than half-deleted.
	if status, _, _ := c.do("GET", "/api/v1/students/stu_penny", nil); status != 200 {
		t.Fatalf("student should survive a refused delete, got %d", status)
	}
}

func TestCascadeDeletesAStudentAndEverythingPointingAtThem(t *testing.T) {
	c := &client{t: t, srv: newServer(t)}
	c.login("admin@jca.ac.th")

	status, res, _ := c.do("DELETE", "/api/v1/students/stu_penny/cascade", nil)
	if status != 200 {
		t.Fatalf("cascade: want 200, got %d (%v)", status, res)
	}
	if res["student"] != "stu_penny" {
		t.Fatalf("want the deleted student named back, got %v", res["student"])
	}
	if res["attendance_rows"].(float64) == 0 {
		t.Fatal("Penny had attendance in the seed; the cascade reported none")
	}

	if status, _, _ := c.do("GET", "/api/v1/students/stu_penny", nil); status != 404 {
		t.Fatalf("student should be gone, got %d", status)
	}

	// Nothing she was referenced by may be left behind.
	for _, path := range []string{
		"/api/v1/attendance?student_id=stu_penny",
		"/api/v1/enrollments?student_id=stu_penny",
		"/api/v1/payments?student_id=stu_penny",
		"/api/v1/practice-activities?student_id=stu_penny",
		"/api/v1/student-parents?student_id=stu_penny",
	} {
		if _, _, list := c.do("GET", path, nil); len(list) != 0 {
			t.Fatalf("%s: %d rows left behind", path, len(list))
		}
	}

	// Her login is gone with her, so it cannot sign in to a portal that has
	// no student behind it.
	_, _, accounts := c.do("GET", "/api/v1/user-accounts", nil)
	for _, a := range accounts {
		if a["user_account_id"] == "usr_penny" {
			t.Fatal("the student's login was left behind")
		}
	}
}

func TestDeletingOneChildKeepsAParentWhoHasAnother(t *testing.T) {
	c := &client{t: t, srv: newServer(t)}
	c.login("admin@jca.ac.th")

	// parent=orphan asks for the guardian too, but only if nobody is left.
	status, res, _ := c.do("DELETE", "/api/v1/students/stu_penny/cascade?parent=orphan", nil)
	if status != 200 {
		t.Fatalf("cascade: want 200, got %d (%v)", status, res)
	}
	if res["parent"] != nil {
		t.Fatalf("Sandy still has Uri; she should have been kept, got %v", res["parent"])
	}
	if status, _, _ := c.do("GET", "/api/v1/parents/par_sandy", nil); status != 200 {
		t.Fatalf("parent with another child should survive, got %d", status)
	}
}

func TestDeletingTheLastChildCanTakeTheParentToo(t *testing.T) {
	c := &client{t: t, srv: newServer(t)}
	c.login("admin@jca.ac.th")

	if status, res, _ := c.do("DELETE", "/api/v1/students/stu_uri/cascade", nil); status != 200 {
		t.Fatalf("first child: want 200, got %d (%v)", status, res)
	}
	status, res, _ := c.do("DELETE", "/api/v1/students/stu_penny/cascade?parent=orphan", nil)
	if status != 200 {
		t.Fatalf("last child: want 200, got %d (%v)", status, res)
	}
	if res["parent"] != "par_sandy" {
		t.Fatalf("the last child's guardian should have gone too, got %v", res["parent"])
	}
	if status, _, _ := c.do("GET", "/api/v1/parents/par_sandy", nil); status != 404 {
		t.Fatalf("parent should be gone, got %d", status)
	}
	// Her contacts and alert preferences go with her.
	if _, _, list := c.do("GET", "/api/v1/parent-contacts?parent_id=par_sandy", nil); len(list) != 0 {
		t.Fatalf("%d contact rows left behind", len(list))
	}
}

func TestDeletingTheLastChildWithoutTheFlagKeepsTheParent(t *testing.T) {
	c := &client{t: t, srv: newServer(t)}
	c.login("admin@jca.ac.th")

	c.do("DELETE", "/api/v1/students/stu_uri/cascade", nil)
	c.do("DELETE", "/api/v1/students/stu_penny/cascade", nil)

	if status, _, _ := c.do("GET", "/api/v1/parents/par_sandy", nil); status != 200 {
		t.Fatalf("without parent=orphan the guardian stays, got %d", status)
	}
}

func TestDeletingAParentKeepsTheChildrenByDefault(t *testing.T) {
	c := &client{t: t, srv: newServer(t)}
	c.login("admin@jca.ac.th")

	status, res, _ := c.do("DELETE", "/api/v1/parents/par_sandy/cascade", nil)
	if status != 200 {
		t.Fatalf("cascade: want 200, got %d (%v)", status, res)
	}
	// Omitted rather than empty when nothing was asked for.
	if res["children"] != nil {
		t.Fatalf("no children were asked for, got %v", res["children"])
	}
	for _, id := range []string{"stu_penny", "stu_uri"} {
		if status, _, _ := c.do("GET", "/api/v1/students/"+id, nil); status != 200 {
			t.Fatalf("%s should survive their parent, got %d", id, status)
		}
	}
	// They are simply unlinked, and can be given a guardian again.
	if _, _, list := c.do("GET", "/api/v1/student-parents", nil); len(list) != 0 {
		t.Fatalf("%d links left pointing at a deleted parent", len(list))
	}
}

func TestDeletingAParentCanTakeTheChildrenToo(t *testing.T) {
	c := &client{t: t, srv: newServer(t)}
	c.login("admin@jca.ac.th")

	status, res, _ := c.do("DELETE", "/api/v1/parents/par_sandy/cascade?children=delete", nil)
	if status != 200 {
		t.Fatalf("cascade: want 200, got %d (%v)", status, res)
	}
	if len(res["children"].([]any)) != 2 {
		t.Fatalf("want both children reported, got %v", res["children"])
	}
	for _, id := range []string{"stu_penny", "stu_uri"} {
		if status, _, _ := c.do("GET", "/api/v1/students/"+id, nil); status != 404 {
			t.Fatalf("%s should be gone, got %d", id, status)
		}
	}
	if _, _, list := c.do("GET", "/api/v1/attendance", nil); len(list) != 0 {
		t.Fatalf("%d attendance rows survived both children", len(list))
	}
}

func TestCascadeIsStaffOnly(t *testing.T) {
	c := &client{t: t, srv: newServer(t)}
	c.login("penny@jca.ac.th") // a Student

	if status, _, _ := c.do("DELETE", "/api/v1/students/stu_uri/cascade", nil); status != 403 {
		t.Fatalf("a student deleting another student: want 403, got %d", status)
	}
	c.login("serene@jca.ac.th") // a Teacher
	if status, _, _ := c.do("DELETE", "/api/v1/parents/par_sandy/cascade", nil); status != 403 {
		t.Fatalf("a teacher deleting a parent: want 403, got %d", status)
	}
	c.token = ""
	if status, _, _ := c.do("DELETE", "/api/v1/students/stu_uri/cascade", nil); status != 401 {
		t.Fatalf("unauthenticated cascade: want 401, got %d", status)
	}
	// And nobody was actually removed by any of those attempts.
	c.login("admin@jca.ac.th")
	if status, _, _ := c.do("GET", "/api/v1/students/stu_uri", nil); status != 200 {
		t.Fatalf("student should be untouched, got %d", status)
	}
}

func TestCascadingAMissingStudentIsANotFound(t *testing.T) {
	c := &client{t: t, srv: newServer(t)}
	c.login("admin@jca.ac.th")

	if status, _, _ := c.do("DELETE", "/api/v1/students/stu_nobody/cascade", nil); status == 200 {
		t.Fatal("deleting a student who does not exist should not report success")
	}
}
