package api_test

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/Kusk24/jtrax-backend/internal/ocr"
)

// A provider whose model can be switched, standing in for Gemini. It records
// which model each scan went to, so "the saved model is used" is asserted
// rather than assumed.
type fakeChooser struct {
	model string
	// Models that answer the test with this status, the way Google answers a
	// withdrawn model with 404.
	failing map[string]int
	scanned *[]string
}

func (f *fakeChooser) Name() string  { return "fake/" + f.model }
func (f *fakeChooser) Model() string { return f.model }
func (f *fakeChooser) WithModel(model string) ocr.Provider {
	if !ocr.ValidModel(model) {
		return f
	}
	c := *f
	c.model = model
	return &c
}
func (f *fakeChooser) Test(context.Context) error {
	if status, bad := f.failing[f.model]; bad {
		return fmt.Errorf("fake: %w", &ocr.StatusError{Status: status})
	}
	return nil
}
func (f *fakeChooser) Extract(context.Context, []byte, string) (*ocr.Form, error) {
	*f.scanned = append(*f.scanned, f.model)
	return &ocr.Form{}, nil
}

func newChooser() *fakeChooser {
	return &fakeChooser{
		model:   "started-with",
		failing: map[string]int{"withdrawn-model": 404, "busy-model": 429},
		scanned: &[]string{},
	}
}

func TestOnlyAnAdminReachesTheScanSettings(t *testing.T) {
	srv := scanServer(t, newChooser())
	parent := &client{t: t, srv: srv}
	parent.login("sandy01234@gmail.com")
	anon := &client{t: t, srv: srv}

	for _, call := range []struct{ method, path string }{
		{"GET", "/api/v1/ocr"},
		{"PUT", "/api/v1/ocr"},
		{"POST", "/api/v1/ocr/test"},
	} {
		if status, _, _ := parent.do(call.method, call.path, map[string]string{"model": "x"}); status != http.StatusForbidden {
			t.Errorf("parent %s %s: want 403, got %d", call.method, call.path, status)
		}
		if status, _, _ := anon.do(call.method, call.path, map[string]string{"model": "x"}); status != http.StatusUnauthorized {
			t.Errorf("anonymous %s %s: want 401, got %d", call.method, call.path, status)
		}
	}
}

func TestTheSavedModelIsTheOneScansUse(t *testing.T) {
	chooser := newChooser()
	srv := scanServer(t, chooser)
	admin := &client{t: t, srv: srv}
	admin.login("admin@jca.ac.th")

	_, state, _ := admin.do("GET", "/api/v1/ocr", nil)
	if state["model"] != "started-with" || state["defaultModel"] != "started-with" || state["configured"] != true {
		t.Fatalf("before saving: %v", state)
	}

	status, state, _ := admin.do("PUT", "/api/v1/ocr", map[string]string{"model": "gemini-3.8-flash"})
	if status != 200 || state["model"] != "gemini-3.8-flash" || state["savedModel"] != "gemini-3.8-flash" {
		t.Fatalf("save: %d %v", status, state)
	}
	// No restart between saving and scanning: the next scan uses it.
	postScan(t, srv, admin.token, "image", onePixelPNG)

	// Empty goes back to the model the server started with.
	if _, state, _ = admin.do("PUT", "/api/v1/ocr", map[string]string{"model": ""}); state["model"] != "started-with" || state["savedModel"] != "" {
		t.Fatalf("reset: %v", state)
	}
	postScan(t, srv, admin.token, "image", onePixelPNG)

	if got := *chooser.scanned; len(got) != 2 || got[0] != "gemini-3.8-flash" || got[1] != "started-with" {
		t.Fatalf("scans went to %v", got)
	}
}

func TestAModelNameThatCouldBendTheURLIsRefused(t *testing.T) {
	srv := scanServer(t, newChooser())
	admin := &client{t: t, srv: srv}
	admin.login("admin@jca.ac.th")

	for _, bad := range []string{"../../v1/files", "gemini?key=x", "Gemini-3.8-Flash", "a/b"} {
		if status, _, _ := admin.do("PUT", "/api/v1/ocr", map[string]string{"model": bad}); status != http.StatusBadRequest {
			t.Errorf("save %q: want 400, got %d", bad, status)
		}
		if status, _, _ := admin.do("POST", "/api/v1/ocr/test", map[string]string{"model": bad}); status != http.StatusBadRequest {
			t.Errorf("test %q: want 400, got %d", bad, status)
		}
	}

	// Written straight into the table through the generic config endpoint,
	// which does not know this key: scans must still not use it.
	admin.do("POST", "/api/v1/system-configuration",
		map[string]string{"config_key": "ocr_model", "config_value": "../../evil"})
	if _, state, _ := admin.do("GET", "/api/v1/ocr", nil); state["model"] != "started-with" {
		t.Fatalf("an invalid saved name reached the provider: %v", state)
	}
}

func TestTheTestButtonSaysWhyAModelFails(t *testing.T) {
	srv := scanServer(t, newChooser())
	admin := &client{t: t, srv: srv}
	admin.login("admin@jca.ac.th")

	cases := []struct {
		model  string
		ok     bool
		reason string
	}{
		{"gemini-3.8-flash", true, ""},
		{"withdrawn-model", false, "model_unavailable"},
		{"busy-model", false, "quota"},
	}
	for _, c := range cases {
		status, got, _ := admin.do("POST", "/api/v1/ocr/test", map[string]string{"model": c.model})
		if status != 200 || got["ok"] != c.ok || got["model"] != c.model {
			t.Errorf("%s: %d %v", c.model, status, got)
		}
		if !c.ok && got["reason"] != c.reason {
			t.Errorf("%s: reason %v, want %s", c.model, got["reason"], c.reason)
		}
	}

	// Trying a model does not save it.
	if _, state, _ := admin.do("GET", "/api/v1/ocr", nil); state["savedModel"] != "" {
		t.Fatalf("a test saved the model: %v", state)
	}

	// With no model named, it tests what scans use now: the saved one.
	admin.do("PUT", "/api/v1/ocr", map[string]string{"model": "withdrawn-model"})
	if _, got, _ := admin.do("POST", "/api/v1/ocr/test", nil); got["ok"] != false || got["model"] != "withdrawn-model" {
		t.Fatalf("testing the saved model: %v", got)
	}
}

func TestTheTestButtonSaysWhenThereIsNoKey(t *testing.T) {
	srv := scanServer(t, nil)
	admin := &client{t: t, srv: srv}
	admin.login("admin@jca.ac.th")

	if _, state, _ := admin.do("GET", "/api/v1/ocr", nil); state["configured"] != false {
		t.Fatalf("no provider but configured: %v", state)
	}
	if _, got, _ := admin.do("POST", "/api/v1/ocr/test", nil); got["ok"] != false || got["reason"] != "not_configured" {
		t.Fatalf("test with no key: %v", got)
	}
}
