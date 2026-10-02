package api_test

import "testing"

// The public pages' footer: what an admin set, readable with no sign-in, and
// nothing else from the configuration table.
func TestAcademyContactIsPublicAndOnlyThat(t *testing.T) {
	srv := newServer(t)
	admin := &client{t: t, srv: srv}
	admin.login("admin@jca.ac.th")
	for key, value := range map[string]string{"academy_phone": "02-123-4567", "academy_email": "hello@jca.example"} {
		if status, out, _ := admin.do("POST", "/api/v1/system-configuration", map[string]any{"config_key": key, "config_value": value}); status != 201 {
			t.Fatalf("set %s: %d (%v)", key, status, out)
		}
	}

	visitor := &client{t: t, srv: srv}
	status, out, _ := visitor.do("GET", "/api/v1/public/academy", nil)
	if status != 200 {
		t.Fatalf("public contact: %d", status)
	}
	if out["phone"] != "02-123-4567" || out["email"] != "hello@jca.example" || out["name"] != "JCA Chess Academy" {
		t.Fatalf("contact: %v", out)
	}
	if _, leaked := out["certificate_sessions"]; leaked || len(out) != 3 {
		t.Fatalf("only the contact fields that are set: %v", out)
	}
}
