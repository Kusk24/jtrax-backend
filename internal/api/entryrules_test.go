package api_test

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

/* An unpaid entry answers to two dates: the early-bird price holds only if
 * paid by the early-bird date, and the place is released if still unpaid once
 * registration closes. */

func day(offset int) string { return time.Now().AddDate(0, 0, offset).Format("2006-01-02") }

// earlyBirdEntry registers one outsider while early bird is open, and returns
// the staff client, the tournament, the entry and its pay code.
func earlyBirdEntry(t *testing.T) (staff, pub *client, tid, reg, code string) {
	t.Helper()
	pub, tid = openEvent(t, map[string]any{
		"regular_fee": 500, "early_bird_fee": 400, "early_bird_deadline": day(10),
		"registration_deadline": day(20),
	})
	staff = &client{t: t, srv: pub.srv}
	staff.login("admin@jca.ac.th")
	s, out := register(t, pub, tid, entry(nil), true)
	if s != 201 || out["feeQuoted"] != float64(400) {
		t.Fatalf("register at the early-bird price: %d (%v)", s, out)
	}
	return staff, pub, tid, out["registrationId"].(string), out["payCode"].(string)
}

// sweep reads the entry through its pay link, which applies the rules first.
func sweep(t *testing.T, pub *client, reg, code string) map[string]any {
	t.Helper()
	s, out, _ := pub.do("POST", "/api/v1/public/tournament-registrations/"+reg, map[string]any{"code": code})
	if s != 200 {
		t.Fatalf("read entry: %d (%v)", s, out)
	}
	return out
}

func TestAnUnpaidEarlyBirdEntryPaysTheRegularPriceOnceItLapses(t *testing.T) {
	staff, pub, tid, reg, code := earlyBirdEntry(t)

	// Still in the window: nothing changes.
	if out := sweep(t, pub, reg, code); out["fee"] != float64(400) {
		t.Fatalf("before the date: want 400, got %v", out["fee"])
	}

	staff.do("PATCH", "/api/v1/tournaments/"+tid, map[string]any{"early_bird_deadline": day(-1)})
	if out := sweep(t, pub, reg, code); out["fee"] != float64(500) || out["state"] != "unpaid" {
		t.Fatalf("after the date: want 500 unpaid, got %v", out)
	}
	_, row, _ := staff.do("GET", "/api/v1/tournament-registrations/"+reg, nil)
	if row["fee_charged"] != float64(500) || row["early_bird_lapsed_at"] == nil {
		t.Fatalf("the entry does not record the lapse: %v", row)
	}
}

func TestAPaidEntryKeepsItsEarlyBirdPrice(t *testing.T) {
	staff, pub, tid, reg, code := earlyBirdEntry(t)
	if s, out, _ := staff.do("POST", "/api/v1/tournament-registrations/"+reg+"/desk-payment",
		map[string]any{"payment_method": "Cash"}); s != 201 && s != 200 {
		t.Fatalf("desk payment: %d (%v)", s, out)
	}
	staff.do("PATCH", "/api/v1/tournaments/"+tid, map[string]any{"early_bird_deadline": day(-1)})
	if out := sweep(t, pub, reg, code); out["fee"] != float64(400) || out["state"] != "paid" {
		t.Fatalf("a paid entry was repriced: %v", out)
	}
}

func TestAnUnpaidPlaceIsReleasedWhenRegistrationCloses(t *testing.T) {
	staff, pub, tid, reg, code := earlyBirdEntry(t)
	// Entered before the closing date, which is now behind us.
	staff.do("PATCH", "/api/v1/tournament-registrations/"+reg, map[string]any{"registered_at": day(-3) + " 10:00:00"})
	staff.do("PATCH", "/api/v1/tournaments/"+tid, map[string]any{"registration_deadline": day(-2)})

	if out := sweep(t, pub, reg, code); out["state"] != "closed" {
		t.Fatalf("want the place released, got %v", out)
	}
	_, row, _ := staff.do("GET", "/api/v1/tournament-registrations/"+reg, nil)
	if row["status"] != "Withdrawn" || row["released_at"] == nil {
		t.Fatalf("the entry does not record the release: %v", row)
	}
	// Staff can give it back, and it stays given back.
	if s, _, _ := staff.do("PATCH", "/api/v1/tournament-registrations/"+reg, map[string]any{"status": "Approved"}); s != 200 {
		t.Fatalf("restore: %d", s)
	}
	if out := sweep(t, pub, reg, code); out["state"] != "unpaid" {
		t.Fatalf("a restored place was released again: %v", out)
	}
}

func TestAnEntryTheDeskAddsAfterClosingIsNotReleased(t *testing.T) {
	staff, pub, tid, reg, code := earlyBirdEntry(t)
	staff.do("PATCH", "/api/v1/tournaments/"+tid, map[string]any{"registration_deadline": day(-2)})
	if out := sweep(t, pub, reg, code); out["state"] != "unpaid" {
		t.Fatalf("an entry made after closing was released: %v", out)
	}
}

func TestTheScanAndTheThaiNameAreKeptWithTheEntry(t *testing.T) {
	pub, tid := openEvent(t, nil)
	staff := &client{t: t, srv: pub.srv}
	staff.login("admin@jca.ac.th")
	// What the card said wins over what the form claims it said.
	s, out := register(t, pub, tid, entry(map[string]any{
		"dateOfBirth": "2015-04-02", "nameTh": "สมชาย นิรันดร์", "documentType": "thai-id",
		"scannedName": "Typed Name", "scannedDateOfBirth": "2015-04-20",
	}), true)
	if s != 201 {
		t.Fatalf("register: %d (%v)", s, out)
	}
	_, row, _ := staff.do("GET", "/api/v1/tournament-registrations/"+out["registrationId"].(string), nil)
	if row["participant_name_th"] != "สมชาย นิรันดร์" || row["id_document_type"] != "thai-id" ||
		row["ocr_name"] != "Read Off Card" || row["ocr_date_of_birth"] != "2015-04-02" ||
		row["participant_date_of_birth"] != "2015-04-02" {
		t.Fatalf("scan not kept: %v", row)
	}
	if s, _ := register(t, pub, tid, entry(map[string]any{
		"email": "x@example.com", "documentType": "driving-licence",
	}), true); s != 400 {
		t.Fatalf("unknown document type: want 400, got %d", s)
	}
}

// U10 at an event dated this year takes anybody born in year-10 or later.
func TestAnAgeGroupGoesByBirthYear(t *testing.T) {
	year := time.Now().Year()
	pub, tid := openEvent(t, map[string]any{"start_date": day(30)})
	staff := &client{t: t, srv: pub.srv}
	staff.login("admin@jca.ac.th")
	eventYear, _ := time.Parse("2006-01-02", day(30))
	year = eventYear.Year()
	u10 := categoryOf(t, staff, tid, "U10")
	for i, tc := range []struct {
		dob  string
		want int
	}{
		{time.Date(year-10, 1, 1, 0, 0, 0, 0, time.UTC).Format("2006-01-02"), 201},
		{time.Date(year-10, 12, 31, 0, 0, 0, 0, time.UTC).Format("2006-01-02"), 201},
		{time.Date(year-11, 12, 31, 0, 0, 0, 0, time.UTC).Format("2006-01-02"), 400},
	} {
		body := entry(map[string]any{"categoryId": u10, "dateOfBirth": tc.dob,
			"email": "dob" + string(rune('a'+i)) + "@example.com"})
		if s, out := register(t, pub, tid, body, true); s != tc.want {
			t.Errorf("born %s: want %d, got %d (%v)", tc.dob, tc.want, s, out)
		}
	}
}

func TestTheCancelPageSaysWhatHappensToAnUnpaidPlace(t *testing.T) {
	_, pub, _, reg, _ := earlyBirdEntry(t)
	res, err := http.Get(pub.srv.URL + "/pay/cancelled?entry=" + reg)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	page := string(b)
	for _, want := range []string{"not paid yet", "early-bird price", "place is released", "jcachess@gmail.com", "lin.ee/7fhq3N1"} {
		if !strings.Contains(page, want) {
			t.Errorf("cancel page is missing %q", want)
		}
	}
	// Any other payment: only that nothing was charged, plus the contacts.
	res, _ = http.Get(pub.srv.URL + "/pay/cancelled")
	b, _ = io.ReadAll(res.Body)
	res.Body.Close()
	if strings.Contains(string(b), "not paid yet") || !strings.Contains(string(b), "nothing was charged") {
		t.Errorf("generic cancel page: %s", b)
	}
}
