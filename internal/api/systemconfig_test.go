// System configuration is display config — the academy's name, the credit
// thresholds, the certificate milestone. The parent portal reads it (the
// child screen counts classes toward certificate_sessions), so every
// signed-in role may read; only staff may write. Secrets live in the
// environment, never in this table, which is what keeps the read safe.
package api_test

import "testing"

func TestAParentCanReadTheConfiguration(t *testing.T) {
	c := &client{t: t, srv: newServer(t)}
	c.login("sandy01234@gmail.com")

	status, _, list := c.do("GET", "/api/v1/system-configuration", nil)
	if status != 200 {
		t.Fatalf("want 200, got %d", status)
	}
	found := false
	for _, row := range list {
		if row["config_key"] == "certificate_sessions" {
			found = true
			if row["config_value"] != "50" {
				t.Fatalf("seeded milestone: want \"50\", got %v", row["config_value"])
			}
		}
	}
	if !found {
		t.Fatalf("certificate_sessions is not in the seed: %v", list)
	}
}

func TestAParentCannotChangeTheConfiguration(t *testing.T) {
	c := &client{t: t, srv: newServer(t)}
	c.login("sandy01234@gmail.com")

	status, _, _ := c.do("PATCH", "/api/v1/system-configuration/certificate_sessions",
		map[string]any{"config_value": "1"})
	if status != 403 {
		t.Fatalf("a parent rewrote the academy's rules: want 403, got %d", status)
	}
}
