// A teacher is a record of who teaches, not an account.
//
// The academy has no teacher workflow — the front desk takes attendance — so
// no teacher signs in anywhere and no teacher portal exists in any front-end.
// The row is still wanted: the Academy screen lists it, and a parent's class
// card names the teacher their child is with.
//
// `teacher.user_account_id` was NOT NULL, so the console had to mint a login
// for every teacher it wrote — a fabricated address and a random password
// nobody was ever shown. 0023 made the column nullable; these are the tests
// that say so.
package api_test

import "testing"

func TestATeacherCanBeWrittenWithoutAnAccount(t *testing.T) {
	c := &client{t: t, srv: newServer(t)}
	c.login("admin@jca.ac.th")

	status, created, _ := c.do("POST", "/api/v1/teachers", map[string]any{
		"name":  "Ms. Ploy",
		"phone": "081-222-3333",
		"email": "ploy@jca.ac.th",
	})
	if status != 201 {
		t.Fatalf("a teacher with no account was refused: %d (%v)", status, created)
	}
	if created["user_account_id"] != nil {
		t.Fatalf("want no account, got %v", created["user_account_id"])
	}

	id := created["teacher_id"].(string)
	status, got, _ := c.do("GET", "/api/v1/teachers/"+id, nil)
	if status != 200 || got["name"] != "Ms. Ploy" || got["email"] != "ploy@jca.ac.th" {
		t.Fatalf("did not survive the round trip: %d %v", status, got)
	}
}

// UNIQUE is still on the column and still does its job. SQLite allows many
// NULLs in a unique column, so a second account-less teacher is fine — which
// is the whole point, since every teacher written from now on has none.
func TestSeveralTeachersMayHaveNoAccount(t *testing.T) {
	c := &client{t: t, srv: newServer(t)}
	c.login("admin@jca.ac.th")

	for _, name := range []string{"Ms. Ploy", "Kru Nok", "Mr. Wit"} {
		status, created, _ := c.do("POST", "/api/v1/teachers", map[string]any{"name": name})
		if status != 201 {
			t.Fatalf("%s: want 201, got %d (%v)", name, status, created)
		}
	}
}

// The teachers already on file keep their link. The migration copies the
// column across rather than clearing it, so nothing that could sign in before
// the deploy stops resolving afterwards — the seeded teacher is the case.
func TestAnExistingTeacherKeepsItsAccount(t *testing.T) {
	c := &client{t: t, srv: newServer(t)}
	c.login("admin@jca.ac.th")

	status, got, _ := c.do("GET", "/api/v1/teachers/tch_serene", nil)
	if status != 200 {
		t.Fatalf("seeded teacher: want 200, got %d (%v)", status, got)
	}
	if got["user_account_id"] != "usr_serene" {
		t.Fatalf("the rebuild dropped the account link: %v", got["user_account_id"])
	}
}
