package api_test

/* The ID card step, on both entry forms: the card is read for the date of
   birth, only what was read is kept, and the entry's age group is decided by
   it. */

import (
	"database/sql"
	"testing"

	"github.com/Kusk24/jtrax-backend/internal/ocr"
)

func cardBorn(dob string) *fakeCardReader {
	return &fakeCardReader{card: &ocr.IDCard{
		FirstName:    ocr.Field{Value: "Penny", Confidence: 0.9},
		LastName:     ocr.Field{Value: "Ward", Confidence: 0.9},
		DateOfBirth:  ocr.Field{Value: dob, Confidence: 0.9},
		DocumentType: "thai-id",
	}}
}

func dbOf(t *testing.T, url string) *sql.DB {
	t.Helper()
	v, ok := serverDBs.Load(url)
	if !ok {
		t.Fatal("no database on record for the server")
	}
	return v.(*sql.DB)
}

func TestAPublicScanKeepsOnlyWhatTheCardSaid(t *testing.T) {
	srv, id := cardServer(t, cardBorn("2016-03-04"))
	status, out := postScan(t, srv, "", "image", onePixelPNG, "/api/v1/public/tournaments/"+id+"/scan-id")
	if status != 200 {
		t.Fatalf("scan: %d (%v)", status, out)
	}
	check, _ := out["checkId"].(string)
	if check == "" {
		t.Fatalf("no checkId in %v", out)
	}
	var dob, name string
	dbOf(t, srv.URL).QueryRow(`SELECT date_of_birth, name FROM id_card_check WHERE id_card_check_id = ?`,
		check).Scan(&dob, &name)
	if dob != "2016-03-04" || name != "Penny Ward" {
		t.Fatalf("kept %q %q, want the card's date of birth and name", dob, name)
	}
}

func TestACardWithoutADateOfBirthVerifiesNothing(t *testing.T) {
	srv, id := cardServer(t, cardBorn(""))
	status, out := postScan(t, srv, "", "image", onePixelPNG, "/api/v1/public/tournaments/"+id+"/scan-id")
	if status != 422 {
		t.Fatalf("want 422, got %d (%v)", status, out)
	}
}

// A parent scans one of their own children's cards, and the entry is decided
// by it: the category must fit the card's birth year, and the check is for
// that child only.
func TestAParentEntryIsVerifiedByTheChildsIDCard(t *testing.T) {
	srv, id := cardServer(t, cardBorn("2016-03-04"))
	staff := &client{t: t, srv: srv}
	staff.login("admin@jca.ac.th")
	u10 := categoryOf(t, staff, id, "Under 10")
	u8 := categoryOf(t, staff, id, "U8")
	parent := &client{t: t, srv: srv}
	parent.login("sandy01234@gmail.com")
	path := "/api/v1/tournaments/" + id + "/scan-id?student_id="

	if status, _ := postScan(t, srv, "", "image", onePixelPNG, path+"stu_penny"); status != 401 {
		t.Fatalf("anonymous scan: want 401, got %d", status)
	}
	if status, _ := postScan(t, srv, parent.token, "image", onePixelPNG, path+"stu_nobody"); status != 404 {
		t.Fatalf("someone else's child: want 404, got %d", status)
	}
	status, out := postScan(t, srv, parent.token, "image", onePixelPNG, path+"stu_penny")
	if status != 200 {
		t.Fatalf("scan: %d (%v)", status, out)
	}
	check := out["checkId"].(string)
	enter := func(student, category string) (int, map[string]any) {
		s, row, _ := parent.do("POST", "/api/v1/tournaments/"+id+"/entries", map[string]any{
			"student_id": student, "tournament_category_id": category, "id_check": check,
		})
		return s, row
	}

	if s, row := enter("stu_uri", u10); s != 400 {
		t.Fatalf("Penny's check for Uri: want 400, got %d (%v)", s, row)
	}
	if s, row := enter("stu_penny", u8); s != 400 {
		t.Fatalf("born 2016 in U8: want 400, got %d (%v)", s, row)
	}
	if s, row, _ := parent.do("POST", "/api/v1/tournaments/"+id+"/entries", map[string]any{
		"student_id": "stu_penny", "tournament_category_id": u10,
	}); s != 400 {
		t.Fatalf("no check: want 400, got %d (%v)", s, row)
	}
	s, row := enter("stu_penny", u10)
	if s != 201 {
		t.Fatalf("enter: %d (%v)", s, row)
	}
	if row["participant_date_of_birth"] != "2016-03-04" || row["tournament_category_id"] != u10 {
		t.Fatalf("entry not decided by the card: %v", row)
	}
}

func TestAParentMustChooseACategoryWhenThereAreSome(t *testing.T) {
	srv, id := cardServer(t, cardBorn("2016-03-04"))
	staff := &client{t: t, srv: srv}
	staff.login("admin@jca.ac.th")
	categoryOf(t, staff, id, "Under 10")
	parent := &client{t: t, srv: srv}
	parent.login("sandy01234@gmail.com")
	_, out := postScan(t, srv, parent.token, "image", onePixelPNG,
		"/api/v1/tournaments/"+id+"/scan-id?student_id=stu_penny")
	if s, row, _ := parent.do("POST", "/api/v1/tournaments/"+id+"/entries", map[string]any{
		"student_id": "stu_penny", "id_check": out["checkId"],
	}); s != 400 {
		t.Fatalf("no category: want 400, got %d (%v)", s, row)
	}
}
