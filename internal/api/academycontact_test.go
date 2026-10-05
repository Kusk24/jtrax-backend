package api_test

import (
	"strings"
	"testing"

	"github.com/Kusk24/jtrax-backend/internal/api"
)

// The public pages' footer: what an admin set, readable with no sign-in, and
// nothing else from the configuration table.
func TestAcademyContactIsPublicAndOnlyThat(t *testing.T) {
	srv := newServer(t)
	admin := &client{t: t, srv: srv}
	admin.login("admin@jca.ac.th")
	for key, value := range map[string]string{"academy_phone": "02-123-4567", "academy_email": "hello@jca.example"} {
		if status, out, _ := admin.do("PATCH", "/api/v1/system-configuration/"+key, map[string]any{"config_value": value}); status != 200 {
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
	if _, leaked := out["certificate_sessions"]; leaked {
		t.Fatalf("a non-contact setting is public: %v", out)
	}
	// The website's details are there from the start (0065).
	if out["instagram"] != "https://www.instagram.com/jcasmartofficial" || !strings.Contains(out["address"].(string), "Paradise Park") {
		t.Fatalf("the starting details are missing: %v", out)
	}
}

// The emails' footer follows what the office saved.
func TestEmailsCarryTheSavedContact(t *testing.T) {
	d := newDB(t)
	srv := newServerOn(t, d)
	admin := &client{t: t, srv: srv}
	admin.login("admin@jca.ac.th")
	admin.do("PATCH", "/api/v1/system-configuration/academy_phone", map[string]any{"config_value": "02-000-0000"})
	admin.do("PATCH", "/api/v1/system-configuration/academy_address", map[string]any{"config_value": "Branch One\nBranch Two"})
	c := api.AcademyContact(d)
	if c.Phone != "02-000-0000" || len(c.Addresses()) != 2 || c.LINE != "https://lin.ee/7fhq3N1" {
		t.Fatalf("contact: %+v", c)
	}
}
