package api_test

import (
	"strings"
	"testing"
)

// A child signs in with an ID, because a child has no mailbox.
//
// The console used to mint `penny.ward@student.jca.ac.th` for every student —
// an address at a domain that receives nothing. This is the whole point of the
// change, so it is asserted end to end: created, stored as given, and actually
// signed in with.
func TestAStudentAccountCanBeCreatedWithAnID(t *testing.T) {
	c := &client{t: t, srv: newServer(t)}
	c.login("admin@jca.ac.th")

	status, obj, _ := c.do("POST", "/api/v1/user-accounts", map[string]string{
		"email": "stu_penny_ward", "password": "chess1234",
		"role": "Student", "display_name": "Penny Ward",
	})
	if status != 201 {
		t.Fatalf("create: %d (%v)", status, obj)
	}
	if obj["email"] != "stu_penny_ward" {
		t.Errorf("stored as %v, want stu_penny_ward", obj["email"])
	}

	fresh := &client{t: t, srv: c.srv}
	if status, obj, _ := fresh.do("POST", "/api/v1/auth/login", map[string]string{
		"email": "stu_penny_ward", "password": "chess1234",
	}); status != 200 {
		t.Fatalf("sign-in with the ID: %d (%v)", status, obj)
	}
}

// Case is the trap an ID inherits from the address it replaced: sign-in
// lower-cases what is typed, so anything stored with capitals is an account
// created and immediately unreachable.
func TestAStudentIDIsStoredLowerCased(t *testing.T) {
	c := &client{t: t, srv: newServer(t)}
	c.login("admin@jca.ac.th")

	status, obj, _ := c.do("POST", "/api/v1/user-accounts", map[string]string{
		"email": "  STU_Penny_Ward ", "password": "chess1234",
		"role": "Student", "display_name": "Penny Ward",
	})
	if status != 201 {
		t.Fatalf("create: %d (%v)", status, obj)
	}
	if obj["email"] != "stu_penny_ward" {
		t.Errorf("stored as %v, want stu_penny_ward", obj["email"])
	}
	fresh := &client{t: t, srv: c.srv}
	if status, _, _ := fresh.do("POST", "/api/v1/auth/login", map[string]string{
		"email": "STU_PENNY_WARD", "password": "chess1234",
	}); status != 200 {
		t.Errorf("sign-in with the ID as typed on a phone keyboard: %d", status)
	}
}

// Two children called John Smith are not one child, and the second must not
// quietly take the first one's account. The UNIQUE index is what makes the
// console's suffix loop safe, so it is asserted here rather than assumed.
func TestASecondChildCannotTakeTheFirstOnesID(t *testing.T) {
	c := &client{t: t, srv: newServer(t)}
	c.login("admin@jca.ac.th")

	body := func(id string) map[string]string {
		return map[string]string{
			"email": id, "password": "chess1234",
			"role": "Student", "display_name": "John Smith",
		}
	}
	if status, obj, _ := c.do("POST", "/api/v1/user-accounts", body("stu_john_smith")); status != 201 {
		t.Fatalf("first John Smith: %d (%v)", status, obj)
	}
	status, obj, _ := c.do("POST", "/api/v1/user-accounts", body("stu_john_smith"))
	if status != 400 {
		t.Fatalf("second John Smith took the same ID: %d (%v)", status, obj)
	}
	// The desk has to be told which of the two things went wrong, because the
	// answer is a different action: a taken ID means try another, and only
	// the console knows the child's name to build one from.
	if msg, _ := obj["error"].(string); !strings.Contains(strings.ToLower(msg), "taken") {
		t.Errorf("the refusal reads %q, which does not say the ID is taken", msg)
	}
	// And the disambiguated one goes through.
	if status, obj, _ := c.do("POST", "/api/v1/user-accounts", body("stu_john_smith_2")); status != 201 {
		t.Errorf("stu_john_smith_2: %d (%v)", status, obj)
	}
}

// Staff and parents reset their own password through a link. A bare ID has no
// mailbox behind it, so creating a receptionist as `desk` would move them into
// the group who need somebody else to let them back in — silently, at the one
// moment anybody was looking.
func TestOnlyAStudentMaySignInWithAnID(t *testing.T) {
	c := &client{t: t, srv: newServer(t)}
	c.login("admin@jca.ac.th")

	for _, role := range []string{"Receptionist", "Admin", "Teacher", "Parent"} {
		status, _, _ := c.do("POST", "/api/v1/user-accounts", map[string]string{
			"email": "desk_" + strings.ToLower(role), "password": "chess1234",
			"role": role, "display_name": role,
		})
		if status != 400 {
			t.Errorf("a %s was created with a bare ID: status %d", role, status)
		}
	}
}

// The role decides the rule, and a PATCH body does not carry one — so it is
// read from the row. Otherwise the check is only as good as the caller's
// honesty about who they are editing.
func TestChangingAnIdentifierUsesTheAccountsOwnRole(t *testing.T) {
	c := &client{t: t, srv: newServer(t)}
	c.login("admin@jca.ac.th")

	_, student, _ := c.do("POST", "/api/v1/user-accounts", map[string]string{
		"email": "stu_penny_ward", "password": "chess1234",
		"role": "Student", "display_name": "Penny Ward",
	})
	_, staff, _ := c.do("POST", "/api/v1/user-accounts", map[string]string{
		"email": "desk@jca.ac.th", "password": "chess1234",
		"role": "Receptionist", "display_name": "Desk",
	})

	studentPath := "/api/v1/user-accounts/" + student["user_account_id"].(string)
	if status, obj, _ := c.do("PATCH", studentPath, map[string]string{"email": "stu_penny_ward_2"}); status != 200 {
		t.Errorf("renaming a child's ID: %d (%v)", status, obj)
	}

	staffPath := "/api/v1/user-accounts/" + staff["user_account_id"].(string)
	if status, _, _ := c.do("PATCH", staffPath, map[string]string{"email": "desk"}); status != 400 {
		t.Errorf("a receptionist was given a bare ID by PATCH: status %d", status)
	}
	// A student can still be given a real address later — an older child who
	// has one should not be stuck with the ID they were registered under.
	if status, obj, _ := c.do("PATCH", studentPath, map[string]string{"email": "penny@gmail.com"}); status != 200 {
		t.Errorf("giving a child a real address: %d (%v)", status, obj)
	}
}

// The reset endpoint answers identically for every input by design, so the
// thing worth asserting is that it does not fall over on an ID — it used to
// hand whatever matched straight to the mail sender.
func TestAskingToResetAnIDIsAcceptedAndSendsNothing(t *testing.T) {
	c := &client{t: t, srv: newServer(t)}
	c.login("admin@jca.ac.th")
	c.do("POST", "/api/v1/user-accounts", map[string]string{
		"email": "stu_penny_ward", "password": "chess1234",
		"role": "Student", "display_name": "Penny Ward",
	})

	anon := &client{t: t, srv: c.srv}
	status, _, _ := anon.do("POST", "/api/v1/auth/forgot-password", map[string]string{
		"email": "stu_penny_ward",
	})
	// Same 202 as any other input: telling an ID apart from an unknown address
	// here would say which identifiers belong to children.
	if status != 202 {
		t.Errorf("forgot-password for an ID: status %d, want 202", status)
	}
}
