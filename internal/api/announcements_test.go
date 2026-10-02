package api_test

import (
	"database/sql"
	"net/http/httptest"
	"testing"
)

// withSecondFamily adds Mark, a parent who can sign in, with one child, Mo,
// in the Intermediate class. Sandy's children are Penny (Beginner) and Uri
// (Intermediate), so a Beginner announcement reaches Sandy and not Mark.
func withSecondFamily(t *testing.T, d *sql.DB) {
	t.Helper()
	for _, q := range []string{
		`INSERT INTO user_account (user_account_id, email, password_hash, role, display_name)
			SELECT 'usr_mark', 'mark@example.com', password_hash, 'Parent', 'Mark Lee'
			  FROM user_account WHERE user_account_id = 'usr_sandy'`,
		`INSERT INTO parent (parent_id, user_account_id, name) VALUES ('par_mark', 'usr_mark', 'Mark Lee')`,
		`INSERT INTO student (student_id, name) VALUES ('stu_mo', 'Mo')`,
		`INSERT INTO student_parent (student_id, parent_id, relationship_type) VALUES ('stu_mo', 'par_mark', 'father')`,
		`INSERT INTO student_enrollment (enrollment_id, student_id, class_id, enrolled_date, status)
			VALUES ('enr_mo', 'stu_mo', 'cls_int101', '2026-01-10', 'Active')`,
	} {
		if _, err := d.Exec(q); err != nil {
			t.Fatalf("second family: %v", err)
		}
	}
}

func announce(t *testing.T, srv *httptest.Server, body map[string]any) (int, map[string]any) {
	t.Helper()
	admin := &client{t: t, srv: srv}
	admin.login("admin@jca.ac.th")
	body["title"] = "Notice"
	body["body"] = "Hello."
	body["author_user_account_id"] = "usr_admin1"
	status, obj, _ := admin.do("POST", "/api/v1/announcements", body)
	return status, obj
}

// visibleTo is how many announcements this account can list.
func visibleTo(t *testing.T, srv *httptest.Server, email string) int {
	t.Helper()
	c := &client{t: t, srv: srv}
	c.login(email)
	_, _, rows := c.do("GET", "/api/v1/announcements", nil)
	return len(rows)
}

// canSee is whether this account's list includes that announcement.
func canSee(t *testing.T, srv *httptest.Server, email, id string) bool {
	t.Helper()
	c := &client{t: t, srv: srv}
	c.login(email)
	_, _, rows := c.do("GET", "/api/v1/announcements", nil)
	for _, r := range rows {
		if r["announcement_id"] == id {
			return true
		}
	}
	return false
}

func TestAnAnnouncementToAllParentsReachesEveryParentAndNoStudent(t *testing.T) {
	d := newDB(t)
	withSecondFamily(t, d)
	srv := newServerOn(t, d)

	if status, obj := announce(t, srv, map[string]any{}); status != 201 {
		t.Fatalf("create: %d %v", status, obj)
	}
	for _, email := range []string{"sandy01234@gmail.com", "mark@example.com"} {
		if got := countType(inbox(t, srv, email), "announcement"); got != 1 {
			t.Fatalf("%s should be told once, got %d", email, got)
		}
	}
	if got := countType(inbox(t, srv, "penny@jca.ac.th"), "announcement"); got != 0 {
		t.Fatalf("students are not an audience any more, penny got %d", got)
	}
	if got := visibleTo(t, srv, "penny@jca.ac.th"); got != 0 {
		t.Fatalf("a student should list no announcements, got %d", got)
	}
}

func TestAnAnnouncementToAClassReachesOnlyItsFamilies(t *testing.T) {
	d := newDB(t)
	withSecondFamily(t, d)
	srv := newServerOn(t, d)

	status, obj := announce(t, srv, map[string]any{
		"audience": "classes", "audience_ids": `["cls_beg101"]`,
	})
	if status != 201 {
		t.Fatalf("create: %d %v", status, obj)
	}
	id := obj["announcement_id"].(string)
	if got := countType(inbox(t, srv, "sandy01234@gmail.com"), "announcement"); got != 1 {
		t.Fatalf("Sandy has a child in Beginner, got %d", got)
	}
	if got := countType(inbox(t, srv, "mark@example.com"), "announcement"); got != 0 {
		t.Fatalf("Mark has no child in Beginner, got %d", got)
	}
	if canSee(t, srv, "mark@example.com", id) {
		t.Fatalf("Mark should not be able to read it either")
	}
	if !canSee(t, srv, "sandy01234@gmail.com", id) {
		t.Fatalf("Sandy should list it")
	}
}

func TestAnAnnouncementToParentsReachesOnlyThoseChosen(t *testing.T) {
	d := newDB(t)
	withSecondFamily(t, d)
	srv := newServerOn(t, d)

	status, obj := announce(t, srv, map[string]any{
		"audience": "parents", "audience_ids": `["par_mark"]`,
	})
	if status != 201 {
		t.Fatalf("create: %d %v", status, obj)
	}
	id := obj["announcement_id"].(string)
	if got := countType(inbox(t, srv, "mark@example.com"), "announcement"); got != 1 {
		t.Fatalf("Mark was chosen, got %d", got)
	}
	if got := countType(inbox(t, srv, "sandy01234@gmail.com"), "announcement"); got != 0 {
		t.Fatalf("Sandy was not chosen, got %d", got)
	}
	if canSee(t, srv, "sandy01234@gmail.com", id) {
		t.Fatalf("Sandy should not list it")
	}
	if !canSee(t, srv, "mark@example.com", id) {
		t.Fatalf("Mark should list it")
	}
}

func TestAClassOrParentAudienceMustNameSomeone(t *testing.T) {
	srv := newServer(t)
	if status, _ := announce(t, srv, map[string]any{"audience": "parents", "audience_ids": `[]`}); status != 400 {
		t.Fatalf("an empty parent list should be refused, got %d", status)
	}
	// A class with nobody's parent in it would reach no one.
	if status, _ := announce(t, srv, map[string]any{"audience": "classes", "audience_ids": `["cls_nobody"]`}); status != 422 {
		t.Fatalf("an audience that resolves to nobody should be refused, got %d", status)
	}
}

// Deleting an announcement takes its recipient list with it.
func TestDeletingATargetedAnnouncementWorks(t *testing.T) {
	d := newDB(t)
	withSecondFamily(t, d)
	srv := newServerOn(t, d)
	_, obj := announce(t, srv, map[string]any{"audience": "parents", "audience_ids": `["par_mark"]`})

	admin := &client{t: t, srv: srv}
	admin.login("admin@jca.ac.th")
	if status, _, _ := admin.do("DELETE", "/api/v1/announcements/"+obj["announcement_id"].(string), nil); status != 204 && status != 200 {
		t.Fatalf("delete: %d", status)
	}
	var left int
	d.QueryRow(`SELECT COUNT(*) FROM announcement_recipient`).Scan(&left)
	if left != 0 {
		t.Fatalf("recipients should go with it, %d left", left)
	}
}
