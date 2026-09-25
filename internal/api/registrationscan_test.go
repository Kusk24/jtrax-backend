package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Kusk24/jtrax-backend/internal/api"
	"github.com/Kusk24/jtrax-backend/internal/mail"
	"github.com/Kusk24/jtrax-backend/internal/ocr"
)

// A fake reader stands in for the vision API: these tests are about the
// endpoint's authorization, validation and shape, none of which should need a
// paid call or a network to verify.
type fakeScanner struct {
	form *ocr.Form
	err  error
	// gotMime records what the handler decided the upload was, so the sniffing
	// can be asserted rather than assumed.
	gotMime string
	calls   int
}

func (f *fakeScanner) Name() string { return "fake" }
func (f *fakeScanner) Extract(_ context.Context, _ []byte, mime string) (*ocr.Form, error) {
	f.calls++
	f.gotMime = mime
	return f.form, f.err
}

// A real 1x1 PNG: the handler sniffs the bytes, so a made-up string would be
// rejected for the wrong reason and prove nothing.
var onePixelPNG = []byte{
	0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a,
	0, 0, 0, 0x0d, 'I', 'H', 'D', 'R',
	0, 0, 0, 1, 0, 0, 0, 1, 8, 6, 0, 0, 0, 0x1f, 0x15, 0xc4, 0x89,
	0, 0, 0, 0x0a, 'I', 'D', 'A', 'T', 0x78, 0x9c, 0x63, 0, 1, 0, 0, 5, 0, 1,
	0x0d, 0x0a, 0x2d, 0xb4, 0, 0, 0, 0, 'I', 'E', 'N', 'D', 0xae, 0x42, 0x60, 0x82,
}

func scanServer(t *testing.T, scanner ocr.Provider) *httptest.Server {
	t.Helper()
	d := newDB(t)
	cfg := mail.Config{}
	srv := httptest.NewServer(api.NewHandlerWith(d, cfg, mail.New(cfg), scanner))
	t.Cleanup(srv.Close)
	return srv
}

// postScan uploads bytes as the named part and returns status plus decoded body.
// path is optional and defaults to the staff form scanner, so the ID-card
// tests can reuse the multipart plumbing without a second copy of it.
func postScan(t *testing.T, srv *httptest.Server, token string, field string, data []byte, path ...string) (int, map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, err := mw.CreateFormFile(field, "form.png")
	if err != nil {
		t.Fatal(err)
	}
	part.Write(data)
	mw.Close()

	target := "/api/v1/registrations/scan"
	if len(path) > 0 {
		target = path[0]
	}
	req, _ := http.NewRequest("POST", srv.URL+target, &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out := map[string]any{}
	json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func tokenFor(t *testing.T, srv *httptest.Server, email string) string {
	t.Helper()
	c := &client{t: t, srv: srv}
	c.login(email)
	return c.token
}

func TestScanRequiresStaff(t *testing.T) {
	scanner := &fakeScanner{form: &ocr.Form{}}
	srv := scanServer(t, scanner)

	// A parent must not be able to post a photograph to a paid vision API.
	status, _ := postScan(t, srv, tokenFor(t, srv, "sandy01234@gmail.com"), "image", onePixelPNG)
	if status != http.StatusForbidden {
		t.Fatalf("parent should be forbidden, got %d", status)
	}
	// And unauthenticated is refused before anything else.
	if status, _ := postScan(t, srv, "", "image", onePixelPNG); status != http.StatusUnauthorized {
		t.Fatalf("anonymous should be unauthorized, got %d", status)
	}
	if scanner.calls != 0 {
		t.Errorf("the provider must not be called for a caller who may not scan, got %d calls", scanner.calls)
	}
}

func TestScanReturnsFieldsAndSavesNothing(t *testing.T) {
	scanner := &fakeScanner{form: ocr.Sanitise(&ocr.Form{
		Name:        ocr.Field{Value: "Somchai Jaidee", Confidence: 0.93},
		DateOfBirth: ocr.Field{Value: "2016-06-02", Confidence: 0.88},
		Courses:     []string{"CHESS"},
	})}
	srv := scanServer(t, scanner)

	status, body := postScan(t, srv, tokenFor(t, srv, "admin@jca.ac.th"), "image", onePixelPNG)
	if status != http.StatusOK {
		t.Fatalf("admin scan: status %d (%v)", status, body)
	}
	if scanner.gotMime != "image/png" {
		t.Errorf("handler should sniff the real type, got %q", scanner.gotMime)
	}
	// The endpoint's whole contract: it suggests, it does not save.
	if body["saved"] != false {
		t.Errorf("scan must not save anything; body said %v", body["saved"])
	}
	fields, ok := body["fields"].(map[string]any)
	if !ok {
		t.Fatalf("no fields in reply: %v", body)
	}
	name := fields["name"].(map[string]any)
	if name["value"] != "Somchai Jaidee" {
		t.Errorf("name not returned: %v", name)
	}
	if _, hasConfidence := name["confidence"]; !hasConfidence {
		t.Error("each field needs a confidence so the console can flag doubtful ones")
	}
}

func TestScanRejectsNonImages(t *testing.T) {
	scanner := &fakeScanner{form: &ocr.Form{}}
	srv := scanServer(t, scanner)
	token := tokenFor(t, srv, "admin@jca.ac.th")

	// A PDF is a plausible mis-pick at the front desk, and costs nothing to
	// refuse here rather than at the provider.
	pdf := []byte("%PDF-1.7\n1 0 obj\n<<>>\nendobj\n")
	if status, _ := postScan(t, srv, token, "image", pdf); status != http.StatusUnsupportedMediaType {
		t.Fatalf("a PDF should be refused, got %d", status)
	}
	if status, _ := postScan(t, srv, token, "image", []byte{}); status != http.StatusBadRequest {
		t.Fatalf("an empty upload should be a 400, got %d", status)
	}
	if status, _ := postScan(t, srv, token, "notimage", onePixelPNG); status != http.StatusBadRequest {
		t.Fatalf("a wrongly named part should be a 400, got %d", status)
	}
	if scanner.calls != 0 {
		t.Errorf("nothing should reach the provider, got %d calls", scanner.calls)
	}
}

func TestScanWithoutAProviderIsUnavailableNotBroken(t *testing.T) {
	// A deployment with no OCR key configured should say scanning is off,
	// rather than fail in a way that reads as a bad photograph.
	srv := scanServer(t, nil)
	status, _ := postScan(t, srv, tokenFor(t, srv, "admin@jca.ac.th"), "image", onePixelPNG)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when scanning is not configured, got %d", status)
	}
}

func TestScanProviderFailureDoesNotLeakInternals(t *testing.T) {
	scanner := &fakeScanner{err: context.DeadlineExceeded}
	srv := scanServer(t, scanner)
	status, body := postScan(t, srv, tokenFor(t, srv, "admin@jca.ac.th"), "image", onePixelPNG)
	if status != http.StatusBadGateway {
		t.Fatalf("a provider failure should be 502, got %d", status)
	}
	msg, _ := body["error"].(string)
	if msg == "" {
		msg, _ = body["message"].(string)
	}
	if msg != "" && bytes.Contains([]byte(msg), []byte("context deadline")) {
		t.Errorf("internal error leaked to the caller: %q", msg)
	}
}

// A body that is not multipart at all must not be reported as an oversized
// image.
//
// This is the bug as it reached a parent. jtrax-web-app's proxy forwarded the
// tournament entry form's upload with Content-Type: application/json, so the
// multipart boundary was gone; ParseMultipartForm failed for that reason, and
// every parse failure was being answered "image is too large (10 MB maximum)".
// A 200 KB photo was therefore met with advice to send a smaller one — which
// could not work, about a problem that did not exist.
func TestScanSaysWhatIsActuallyWrongWithAMalformedUpload(t *testing.T) {
	scanner := &fakeScanner{form: &ocr.Form{}}
	srv := scanServer(t, scanner)
	token := tokenFor(t, srv, "admin@jca.ac.th")

	// Exactly what the broken proxy produced: real image bytes, wrong type.
	req, _ := http.NewRequest("POST", srv.URL+"/api/v1/registrations/scan", bytes.NewReader(onePixelPNG))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out := map[string]any{}
	json.NewDecoder(resp.Body).Decode(&out)

	if resp.StatusCode == http.StatusRequestEntityTooLarge {
		t.Fatal("a 70-byte upload was reported as too large")
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (%v)", resp.StatusCode, out)
	}
	msg, _ := out["error"].(string)
	if strings.Contains(strings.ToLower(msg), "too large") {
		t.Errorf("error still blames the size: %q", msg)
	}
	if !strings.Contains(msg, "multipart") {
		t.Errorf("error does not say what was actually wrong: %q", msg)
	}
	// Nothing reached the paid provider.
	if scanner.calls != 0 {
		t.Errorf("provider was called %d times for an unparseable upload", scanner.calls)
	}
}

// And the size limit still reports itself as a size limit — the point is to
// tell the two apart, not to stop saying "too large" when it is true.
func TestScanStillRefusesAnOversizedImage(t *testing.T) {
	srv := scanServer(t, &fakeScanner{form: &ocr.Form{}})
	token := tokenFor(t, srv, "admin@jca.ac.th")

	// 11 MB against the 10 MB cap.
	status, out := postScan(t, srv, token, "image", bytes.Repeat([]byte{0x41}, 11<<20))
	if status != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413 (%v)", status, out)
	}
	if msg, _ := out["error"].(string); !strings.Contains(msg, "too large") {
		t.Errorf("error = %q, want it to name the size", msg)
	}
}
