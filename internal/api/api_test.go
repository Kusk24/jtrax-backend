package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/Kusk24/jtrax-backend/internal/api"
	"github.com/Kusk24/jtrax-backend/internal/db"
)

type client struct {
	t     *testing.T
	srv   *httptest.Server
	token string
}

func (c *client) do(method, path string, body any) (int, map[string]any, []map[string]any) {
	var buf bytes.Buffer
	if body != nil {
		json.NewEncoder(&buf).Encode(body)
	}
	req, _ := http.NewRequest(method, c.srv.URL+path, &buf)
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw := json.RawMessage{}
	json.NewDecoder(resp.Body).Decode(&raw)
	obj := map[string]any{}
	list := []map[string]any{}
	if len(raw) > 0 && raw[0] == '[' {
		json.Unmarshal(raw, &list)
	} else if len(raw) > 0 {
		json.Unmarshal(raw, &obj)
	}
	return resp.StatusCode, obj, list
}

func (c *client) login(email string) {
	status, obj, _ := c.do("POST", "/api/v1/auth/login", map[string]string{
		"email": email, "password": db.DevPassword,
	})
	if status != 200 {
		c.t.Fatalf("login %s: status %d (%v)", email, status, obj)
	}
	c.token = obj["token"].(string)
}

func newServer(t *testing.T) *httptest.Server {
	d, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if err := db.Seed(d, db.DevPassword); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(api.NewHandler(d))
	t.Cleanup(srv.Close)
	return srv
}

func TestLoginRejectsWrongPassword(t *testing.T) {
	c := &client{t: t, srv: newServer(t)}
	status, _, _ := c.do("POST", "/api/v1/auth/login", map[string]string{
		"email": "admin@jca.ac.th", "password": "wrong",
	})
	if status != 401 {
		t.Fatalf("want 401, got %d", status)
	}
}

func TestUnauthenticatedRequestsRejected(t *testing.T) {
	c := &client{t: t, srv: newServer(t)}
	status, _, _ := c.do("GET", "/api/v1/students", nil)
	if status != 401 {
		t.Fatalf("want 401, got %d", status)
	}
}

func TestAdminCRUDLifecycle(t *testing.T) {
	c := &client{t: t, srv: newServer(t)}
	c.login("admin@jca.ac.th")

	status, created, _ := c.do("POST", "/api/v1/classes", map[string]any{
		"name": "Master Class", "class_type": "Master",
	})
	if status != 201 {
		t.Fatalf("create: want 201, got %d (%v)", status, created)
	}
	id := created["class_id"].(string)

	status, _, list := c.do("GET", "/api/v1/classes", nil)
	if status != 200 || len(list) != 3 {
		t.Fatalf("list: want 3 classes, got %d (status %d)", len(list), status)
	}

	status, updated, _ := c.do("PATCH", "/api/v1/classes/"+id, map[string]any{
		"description": "Invite only",
	})
	if status != 200 || updated["description"] != "Invite only" {
		t.Fatalf("update failed: %d %v", status, updated)
	}

	status, bad, _ := c.do("PATCH", "/api/v1/classes/"+id, map[string]any{"class_type": "Bogus"})
	if status != 400 {
		t.Fatalf("enum validation: want 400, got %d (%v)", status, bad)
	}

	status, _, _ = c.do("DELETE", "/api/v1/classes/"+id, nil)
	if status != 200 {
		t.Fatalf("delete: want 200, got %d", status)
	}
}

func TestParentSeesOnlyOwnChildren(t *testing.T) {
	c := &client{t: t, srv: newServer(t)}
	c.login("admin@jca.ac.th")
	// A student that does not belong to Sandy.
	status, other, _ := c.do("POST", "/api/v1/students", map[string]any{"name": "Somchai"})
	if status != 201 {
		t.Fatalf("create student: %d", status)
	}

	c.login("sandy01234@gmail.com")
	_, _, students := c.do("GET", "/api/v1/students", nil)
	if len(students) != 2 {
		t.Fatalf("parent should see exactly her 2 children, got %d", len(students))
	}
	status, _, _ = c.do("GET", "/api/v1/students/"+other["student_id"].(string), nil)
	if status != 404 {
		t.Fatalf("foreign student should be invisible (404), got %d", status)
	}
	// Parents cannot create students at all.
	status, _, _ = c.do("POST", "/api/v1/students", map[string]any{"name": "Hack"})
	if status != 403 {
		t.Fatalf("parent create student: want 403, got %d", status)
	}
	// Payments are scoped to her children.
	_, _, payments := c.do("GET", "/api/v1/payments", nil)
	if len(payments) != 2 {
		t.Fatalf("parent payments: want 2, got %d", len(payments))
	}
}

func TestParentRegistersOwnChildOnly(t *testing.T) {
	c := &client{t: t, srv: newServer(t)}
	c.login("sandy01234@gmail.com")
	status, _, _ := c.do("POST", "/api/v1/tournament-registrations", map[string]any{
		"tournament_id": "trn_wellington", "student_id": "stu_penny",
		"participant_name": "Penny", "fee_charged": 300,
	})
	if status != 201 {
		t.Fatalf("register own child: want 201, got %d", status)
	}
	status, _, _ = c.do("POST", "/api/v1/tournament-registrations", map[string]any{
		"tournament_id": "trn_wellington", "student_id": "stu_missing",
		"participant_name": "Nope",
	})
	if status != 403 {
		t.Fatalf("register foreign child: want 403, got %d", status)
	}
}

func TestStudentLogsPracticeForSelfOnly(t *testing.T) {
	c := &client{t: t, srv: newServer(t)}
	c.login("penny@jca.ac.th")
	status, _, _ := c.do("POST", "/api/v1/practice-activities", map[string]any{
		"student_id": "stu_penny", "activity_date": "2026-05-11",
		"minutes_practiced": 20, "puzzles_completed": 3,
	})
	if status != 201 {
		t.Fatalf("own practice: want 201, got %d", status)
	}
	status, _, _ = c.do("POST", "/api/v1/practice-activities", map[string]any{
		"student_id": "stu_uri", "activity_date": "2026-05-11",
	})
	if status != 403 {
		t.Fatalf("other student practice: want 403, got %d", status)
	}
	// Students cannot touch staff resources.
	status, _, _ = c.do("GET", "/api/v1/user-accounts", nil)
	if status != 403 {
		t.Fatalf("student user-accounts: want 403, got %d", status)
	}
}

func TestAccountEndpointNeverLeaksHashes(t *testing.T) {
	c := &client{t: t, srv: newServer(t)}
	c.login("admin@jca.ac.th")
	_, _, accounts := c.do("GET", "/api/v1/user-accounts", nil)
	if len(accounts) == 0 {
		t.Fatal("expected seeded accounts")
	}
	for _, a := range accounts {
		if _, leaked := a["password_hash"]; leaked {
			t.Fatal("password_hash must never be serialized")
		}
	}
}
