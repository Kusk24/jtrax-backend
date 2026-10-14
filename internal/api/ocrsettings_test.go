package api_test

import (
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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

// A chooser whose key can also list models, the way Gemini's can.
type fakeLister struct {
	*fakeChooser
	models []ocr.ModelInfo
	err    error
	calls  int
}

func (f *fakeLister) ListModels(context.Context) ([]ocr.ModelInfo, error) {
	f.calls++
	return f.models, f.err
}

// Settings offers Google's own list, each with its price, so an admin sees
// what a model costs before choosing it.
func TestTheModelListComesFromGoogleWithPrices(t *testing.T) {
	lister := &fakeLister{fakeChooser: newChooser(), models: []ocr.ModelInfo{
		{Name: "gemini-3.8-flash", DisplayName: "Gemini 3.8 Flash"},
		{Name: "gemini-3.7-flash", DisplayName: "Gemini 3.7 Flash"},
	}}
	srv := scanServer(t, lister)
	admin := &client{t: t, srv: srv}
	admin.login("admin@jca.ac.th")

	status, out, _ := admin.do("GET", "/api/v1/ocr/models", nil)
	if status != 200 {
		t.Fatalf("status %d (%v)", status, out)
	}
	models, _ := out["models"].([]any)
	if len(models) != 2 {
		t.Fatalf("models = %v", out["models"])
	}
	first := models[0].(map[string]any)
	price, _ := first["price"].(map[string]any)
	if first["name"] != "gemini-3.8-flash" || price["inputPerMillion"] != 0.75 || price["outputPerMillion"] != 3.75 {
		t.Errorf("first = %v, want 3.8-flash at 0.75 / 3.75", first)
	}
	if _, has := models[1].(map[string]any)["price"]; has {
		t.Errorf("a model with no price on file must show none: %v", models[1])
	}
	if out["pricesAsOf"] != ocr.PricesAsOf || out["pricesSource"] != ocr.PricesSource {
		t.Errorf("where the prices come from must be said: %v", out)
	}

	// Asked again within ten minutes, Google is not.
	admin.do("GET", "/api/v1/ocr/models", nil)
	if lister.calls != 1 {
		t.Errorf("want one call to Google, got %d", lister.calls)
	}
}

func TestAModelListGoogleRefusedSaysWhy(t *testing.T) {
	lister := &fakeLister{fakeChooser: newChooser(), err: fmt.Errorf("gemini: %w", &ocr.StatusError{Status: 403})}
	srv := scanServer(t, lister)
	admin := &client{t: t, srv: srv}
	admin.login("admin@jca.ac.th")

	status, out, _ := admin.do("GET", "/api/v1/ocr/models", nil)
	if status != 200 || out["reason"] != "key_rejected" || out["status"] != float64(403) {
		t.Fatalf("got %d %v, want 200 key_rejected 403", status, out)
	}
}

func TestOnlyAnAdminSeesTheModelList(t *testing.T) {
	srv := scanServer(t, &fakeLister{fakeChooser: newChooser()})
	parent := &client{t: t, srv: srv}
	parent.login("sandy01234@gmail.com")
	if status, _, _ := parent.do("GET", "/api/v1/ocr/models", nil); status != http.StatusForbidden {
		t.Errorf("parent: want 403, got %d", status)
	}
	if status, _, _ := (&client{t: t, srv: srv}).do("GET", "/api/v1/ocr/models", nil); status != http.StatusUnauthorized {
		t.Errorf("anonymous: want 401, got %d", status)
	}
}

// What scanning costs, in Settings: each call Google answered is kept with its
// tokens and priced at the model's price on the day.
func TestSettingsSayWhatScanningHasCost(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"candidates":[{"content":{"parts":[{"text":"{}"}]}}],
		  "usageMetadata":{"promptTokenCount":2000,"candidatesTokenCount":300,"thoughtsTokenCount":500}}`)
	}))
	t.Cleanup(stub.Close)
	srv := scanServer(t, ocr.New(ocr.Config{Provider: "gemini", APIKey: "test-key", BaseURL: stub.URL}))
	admin := &client{t: t, srv: srv}
	admin.login("admin@jca.ac.th")

	if status, _ := postScan(t, srv, admin.token, "image", onePixelPNG); status != 200 {
		t.Fatalf("scan: %d", status)
	}
	admin.do("POST", "/api/v1/ocr/test", nil)

	_, state, _ := admin.do("GET", "/api/v1/ocr", nil)
	usage, _ := state["usage"].(map[string]any)
	if usage["scans"] != float64(1) || usage["tests"] != float64(1) {
		t.Fatalf("usage = %v, want 1 scan and 1 test", usage)
	}
	if usage["inputTokens"] != float64(4000) || usage["outputTokens"] != float64(1600) {
		t.Errorf("tokens = %v in, %v out; want 4000 and 1600", usage["inputTokens"], usage["outputTokens"])
	}
	price, _, _ := ocr.PriceOn(state["model"].(string), time.Now().UTC())
	want := (4000*price.InputPerMillion + 1600*price.OutputPerMillion) / 1e6
	if got, _ := usage["costUSD"].(float64); math.Abs(got-want) > 1e-9 {
		t.Errorf("costUSD = %v, want %v", got, want)
	}
	// The average scan leaves the test out.
	if usage["avgScanInputTokens"] != float64(2000) || usage["avgScanOutputTokens"] != float64(800) {
		t.Errorf("average scan = %v in, %v out; want 2000 and 800", usage["avgScanInputTokens"], usage["avgScanOutputTokens"])
	}
}
