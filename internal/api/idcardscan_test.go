package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Kusk24/jtrax-backend/internal/mail"

	"github.com/Kusk24/jtrax-backend/internal/api"
	"github.com/Kusk24/jtrax-backend/internal/ocr"
)

/* Reading a player's ID card on the *public* entry form.
 *
 * This is the first unauthenticated endpoint in the product that costs money —
 * everything else a stranger can reach is a database read or a row insert. So
 * the tests here are mostly about what bounds it rather than what it extracts.
 */

// fakeCardReader is a provider that can also read a card.
type fakeCardReader struct {
	card    *ocr.IDCard
	err     error
	gotMime string
	calls   int
}

func (f *fakeCardReader) Name() string { return "fake-card" }
func (f *fakeCardReader) Extract(context.Context, []byte, string) (*ocr.Form, error) {
	return &ocr.Form{}, nil
}
func (f *fakeCardReader) ExtractIDCard(_ context.Context, _ []byte, mime string) (*ocr.IDCard, error) {
	f.calls++
	f.gotMime = mime
	return f.card, f.err
}

// formOnlyScanner reads the academy's form but not a card — the capability is
// a separate interface precisely so this is representable.
type formOnlyScanner struct{ fakeScanner }

func cardServer(t *testing.T, p ocr.Provider) (*httptest.Server, string) {
	t.Helper()
	d := newDB(t)
	cfg := mail.Config{}
	srv := httptest.NewServer(api.NewHandlerWith(d, cfg, mail.New(cfg), p))
	t.Cleanup(srv.Close)

	staff := &client{t: t, srv: srv}
	staff.login("admin@jca.ac.th")
	_, tour, _ := staff.do("POST", "/api/v1/tournaments", map[string]any{
		"name": "JCA Chessfest", "public_registration": true, "regular_fee": 1000,
	})
	return srv, tour["tournament_id"].(string)
}

// The gate that makes this endpoint acceptable at all: it exists only while the
// academy is actually taking entries.
func TestScanningACardNeedsAnOpenTournament(t *testing.T) {
	reader := &fakeCardReader{card: &ocr.IDCard{}}
	srv, id := cardServer(t, reader)

	staff := &client{t: t, srv: srv}
	staff.login("admin@jca.ac.th")
	staff.do("PATCH", "/api/v1/tournaments/"+id, map[string]any{"public_registration": false})

	status, _ := postScan(t, srv, "", "image", onePixelPNG, "/api/v1/public/tournaments/"+id+"/scan-id")
	// 404 rather than 403, like the entry form: a closed event must not be
	// distinguishable from one that does not exist.
	if status != 404 {
		t.Fatalf("closed event: want 404, got %d", status)
	}
	if reader.calls != 0 {
		t.Errorf("a closed event must not reach the paid provider, got %d calls", reader.calls)
	}
}

func TestScanningACardIsRefusedForAnUnknownTournament(t *testing.T) {
	reader := &fakeCardReader{card: &ocr.IDCard{}}
	srv, _ := cardServer(t, reader)
	status, _ := postScan(t, srv, "", "image", onePixelPNG, "/api/v1/public/tournaments/trn_nope/scan-id")
	if status != 404 {
		t.Fatalf("unknown event: want 404, got %d", status)
	}
	if reader.calls != 0 {
		t.Errorf("want no provider call, got %d", reader.calls)
	}
}

// Scanning is a convenience. A provider that cannot read a card must leave the
// entrant typing, not leave them unable to enter.
func TestAProviderThatCannotReadCardsSaysSo(t *testing.T) {
	srv, id := cardServer(t, &formOnlyScanner{})
	status, out := postScan(t, srv, "", "image", onePixelPNG, "/api/v1/public/tournaments/"+id+"/scan-id")
	if status != 503 {
		t.Fatalf("want 503, got %d (%v)", status, out)
	}
}

func TestScanningACardReturnsTheFieldsWithoutStoringTheImage(t *testing.T) {
	reader := &fakeCardReader{card: ocr.SanitiseIDCard(&ocr.IDCard{
		FirstName:    ocr.Field{Value: "Somchai", Confidence: 0.94},
		LastName:     ocr.Field{Value: "Jaidee", Confidence: 0.91},
		DateOfBirth:  ocr.Field{Value: "2540-05-02", Confidence: 0.88},
		DocumentType: "thai-id",
	})}
	srv, id := cardServer(t, reader)

	status, out := postScan(t, srv, "", "image", onePixelPNG, "/api/v1/public/tournaments/"+id+"/scan-id")
	if status != 200 {
		t.Fatalf("scan: %d (%v)", status, out)
	}
	fields, _ := out["fields"].(map[string]any)
	dob, _ := fields["dateOfBirth"].(map[string]any)
	// The Buddhist-era year is converted before it ever reaches the form.
	if dob["value"] != "1997-05-02" {
		t.Errorf("dateOfBirth = %v, want 1997-05-02", dob["value"])
	}
	if out["provider"] != "fake-card" {
		t.Errorf("provider = %v — it must be answerable which service saw the document", out["provider"])
	}
	// The bytes are sniffed, not taken on the client's word.
	if reader.gotMime != "image/png" {
		t.Errorf("gotMime = %q, want image/png", reader.gotMime)
	}
}

// A provider failure is not the entrant's fault and they must still be able to
// enter by typing — so it is an error they can read, not a 500.
func TestAnUnreadableCardIsSaidPlainly(t *testing.T) {
	reader := &fakeCardReader{err: context.DeadlineExceeded}
	srv, id := cardServer(t, reader)
	status, _ := postScan(t, srv, "", "image", onePixelPNG, "/api/v1/public/tournaments/"+id+"/scan-id")
	if status != 502 {
		t.Fatalf("want 502, got %d", status)
	}
}

func TestScanningACardRefusesSomethingThatIsNotAnImage(t *testing.T) {
	reader := &fakeCardReader{card: &ocr.IDCard{}}
	srv, id := cardServer(t, reader)
	status, _ := postScan(t, srv, "", "image", []byte("%PDF-1.4 not an image"),
		"/api/v1/public/tournaments/"+id+"/scan-id")
	if status != 415 {
		t.Fatalf("want 415, got %d", status)
	}
	if reader.calls != 0 {
		t.Errorf("a mis-picked file must cost nothing at the provider, got %d calls", reader.calls)
	}
}

// The bug a parent actually hit, at the endpoint they hit it on.
//
// jtrax-web-app's proxy forwarded this upload with Content-Type:
// application/json, which dropped the multipart boundary. Parsing then failed,
// and every parse failure here was answered "image is too large (10 MB
// maximum)" — so a 200 KB photo of a child's ID card was met with advice to
// send a smaller one. The proxy is fixed; this is the half that made the
// failure unreadable.
func TestAMalformedCardUploadIsNotCalledTooLarge(t *testing.T) {
	reader := &fakeCardReader{card: &ocr.IDCard{FirstName: ocr.Field{Value: "Ada", Confidence: 0.9}}}
	srv, id := cardServer(t, reader)

	req, _ := http.NewRequest("POST",
		srv.URL+"/api/v1/public/tournaments/"+id+"/scan-id",
		bytes.NewReader(onePixelPNG))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out := map[string]any{}
	json.NewDecoder(resp.Body).Decode(&out)

	if resp.StatusCode == http.StatusRequestEntityTooLarge {
		t.Fatal("a 70-byte card upload was reported as too large")
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (%v)", resp.StatusCode, out)
	}
	if msg, _ := out["error"].(string); strings.Contains(strings.ToLower(msg), "too large") {
		t.Errorf("error still blames the size: %q", msg)
	}
	// An unparseable request must not reach the paid provider.
	if reader.calls != 0 {
		t.Errorf("provider was called %d times for an unparseable upload", reader.calls)
	}
}

// A properly-formed multipart upload — what the fixed proxy now sends — still
// works. The pair is the point: the same photo fails one way and succeeds the
// other, and only the envelope differs.
func TestAWellFormedCardUploadStillReads(t *testing.T) {
	reader := &fakeCardReader{card: &ocr.IDCard{
		FirstName: ocr.Field{Value: "Ada", Confidence: 0.9},
		LastName:  ocr.Field{Value: "Lovelace", Confidence: 0.9},
	}}
	srv, id := cardServer(t, reader)

	status, out := postScan(t, srv, "", "image", onePixelPNG,
		"/api/v1/public/tournaments/"+id+"/scan-id")
	if status != 200 {
		t.Fatalf("status = %d, want 200 (%v)", status, out)
	}
	fields, _ := out["fields"].(map[string]any)
	first, _ := fields["firstName"].(map[string]any)
	if first["value"] != "Ada" {
		t.Errorf("fields = %v, want the card read back", fields)
	}
	if reader.gotMime != "image/png" {
		t.Errorf("provider saw mime %q, want image/png", reader.gotMime)
	}
}
