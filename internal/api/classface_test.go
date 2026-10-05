// The icon and level a class is shown with.
//
// The Academy screen has offered an icon picker and a badge field since it was
// built, and there was nowhere to put either: the console re-derived both from
// class_type on every render, so choosing the pawn and pressing Save changed
// nothing. These are the columns that make the choice survive.
package api_test

import "testing"

func TestClassKeepsItsIconAndLevel(t *testing.T) {
	c := &client{t: t, srv: newServer(t)}
	c.login("admin@jca.ac.th")

	status, created, _ := c.do("POST", "/api/v1/classes", map[string]any{
		"name":  "Saturday Juniors",
		"icon":  "pawn",
		"level": "Intermediate",
	})
	if status != 201 {
		t.Fatalf("create: want 201, got %d (%v)", status, created)
	}
	if created["icon"] != "pawn" || created["level"] != "Intermediate" {
		t.Fatalf("icon and level were discarded on create: %v", created)
	}

	id := created["class_id"].(string)

	status, got, _ := c.do("GET", "/api/v1/classes/"+id, nil)
	if status != 200 || got["icon"] != "pawn" || got["level"] != "Intermediate" {
		t.Fatalf("did not survive the round trip: %d %v", status, got)
	}

	// The report was about editing, not creating: the office picks a different
	// piece on a class that already exists and the screen goes on showing the
	// old one.
	status, edited, _ := c.do("PATCH", "/api/v1/classes/"+id, map[string]any{
		"icon":  "knight",
		"level": "Advanced",
	})
	if status != 200 || edited["icon"] != "knight" || edited["level"] != "Advanced" {
		t.Fatalf("edit: %d (%v)", status, edited)
	}

	// Renaming must not quietly reset the face. A PATCH carries only what
	// changed, and the columns it does not mention are left alone.
	status, renamed, _ := c.do("PATCH", "/api/v1/classes/"+id, map[string]any{"name": "Sunday Juniors"})
	if status != 200 || renamed["icon"] != "knight" || renamed["level"] != "Advanced" {
		t.Fatalf("rename cleared the icon or level: %d (%v)", status, renamed)
	}
}

// The icon is not an enum on purpose — the names come from the console's own
// icon set, which moves with the design. A CHECK here would mean a migration
// every time somebody adds a piece to the picker.
func TestClassIconIsNotConstrainedToAList(t *testing.T) {
	c := &client{t: t, srv: newServer(t)}
	c.login("admin@jca.ac.th")

	status, created, _ := c.do("POST", "/api/v1/classes", map[string]any{
		"name": "Endgame Lab",
		"icon": "bishop",
	})
	if status != 201 || created["icon"] != "bishop" {
		t.Fatalf("a name the backend has never heard of should still store: %d (%v)", status, created)
	}
}

// A class arrives with a face on it.
//
// 0022 backfills the classes that predate it, which a freshly migrated
// database never has — so the seed has to write both itself, or every class
// the developer sees is the one case the console has to fall back for.
func TestSeededClassesHaveAnIconAndALevel(t *testing.T) {
	c := &client{t: t, srv: newServer(t)}
	c.login("admin@jca.ac.th")

	status, _, rows := c.do("GET", "/api/v1/classes", nil)
	if status != 200 {
		t.Fatalf("list classes: %d", status)
	}
	if len(rows) == 0 {
		t.Fatal("the seed has no classes to check")
	}

	for _, row := range rows {
		if row["icon"] == nil || row["icon"] == "" {
			t.Errorf("%v has no icon", row["name"])
		}
		if row["level"] == nil || row["level"] == "" {
			t.Errorf("%v has no level", row["name"])
		}
	}
}

// A course is a name, a level and a type: the level is one of three, and
// "Master" is no longer a type (0066).
func TestACourseHasALevelAndAPrivateOrGroupType(t *testing.T) {
	c := &client{t: t, srv: newServer(t)}
	c.login("admin@jca.ac.th")
	if s, out, _ := c.do("POST", "/api/v1/classes", map[string]any{"name": "JCA NXT", "level": "Expert"}); s != 400 {
		t.Fatalf("an unknown level: want 400, got %d (%v)", s, out)
	}
	if s, out, _ := c.do("POST", "/api/v1/classes", map[string]any{"name": "JCA NXT", "class_type": "Master"}); s != 400 {
		t.Fatalf("the Master type: want 400, got %d (%v)", s, out)
	}
	s, out, _ := c.do("POST", "/api/v1/classes", map[string]any{"name": "JCA NXT", "level": "Advanced", "class_type": "Private", "price_per_credit": 600})
	if s != 201 || out["level"] != "Advanced" || out["class_type"] != "Private" || out["price_per_credit"] != float64(600) {
		t.Fatalf("create: %d (%v)", s, out)
	}
	if _, has := out["badge"]; has {
		t.Fatalf("the badge is still there: %v", out)
	}
}
