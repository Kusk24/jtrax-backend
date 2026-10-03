package ocr

import "testing"

// Every case here is something a model actually can return. The sanitiser is
// the only thing between that and a pre-filled form, so it is tested harder
// than the transport around it.

func TestSanitiseTrimsAndZeroesEmptyValues(t *testing.T) {
	f := Sanitise(&Form{
		Name:  Field{Value: "  Somchai  ", Confidence: 0.9},
		Email: Field{Value: "   ", Confidence: 0.8},
	})
	if f.Name.Value != "Somchai" {
		t.Errorf("name not trimmed: %q", f.Name.Value)
	}
	if f.Email.Value != "" || f.Email.Confidence != 0 {
		t.Errorf("a blank line is not a confident reading: %+v", f.Email)
	}
}

func TestSanitiseClampsConfidence(t *testing.T) {
	f := Sanitise(&Form{
		Name:    Field{Value: "A", Confidence: 4.2},
		Address: Field{Value: "B", Confidence: -1},
	})
	if f.Name.Confidence != 1 {
		t.Errorf("confidence above 1 not clamped: %v", f.Name.Confidence)
	}
	if f.Address.Confidence != 0 {
		t.Errorf("negative confidence not clamped: %v", f.Address.Confidence)
	}
}

func TestSanitiseDateOfBirth(t *testing.T) {
	ok := Sanitise(&Form{DateOfBirth: Field{Value: "2016-06-02", Confidence: 0.9}})
	if ok.DateOfBirth.Confidence != 0.9 {
		t.Errorf("a valid ISO date should keep its confidence: %v", ok.DateOfBirth)
	}

	// The staff member can still see and fix what the parent wrote; it just
	// must not arrive looking trustworthy.
	bad := Sanitise(&Form{DateOfBirth: Field{Value: "02/06/16", Confidence: 0.95}})
	if bad.DateOfBirth.Value != "02/06/16" {
		t.Errorf("an unparseable date should be kept for correction, got %q", bad.DateOfBirth.Value)
	}
	if bad.DateOfBirth.Confidence != 0 {
		t.Errorf("an unparseable date must not look confident: %v", bad.DateOfBirth.Confidence)
	}
}

func TestSanitisePhoneKeepsLeadingZeroDropsJunk(t *testing.T) {
	f := Sanitise(&Form{ContactNumber: Field{Value: " (081) 234-5678 ext", Confidence: 0.8}})
	got := f.ContactNumber.Value
	if got != "081 234-5678" {
		t.Errorf("phone not cleaned as expected: %q", got)
	}
}

func TestSanitiseCoursesKeepOnlyWhatIsPrintedOnTheForm(t *testing.T) {
	f := Sanitise(&Form{Courses: []string{"coding", "MUSIC", "chess", "CHESS"}})
	want := []string{"CHESS", "CODING"}
	if len(f.Courses) != len(want) {
		t.Fatalf("courses = %v, want %v", f.Courses, want)
	}
	for i := range want {
		if f.Courses[i] != want[i] {
			t.Fatalf("courses = %v, want %v (form order, deduped)", f.Courses, want)
		}
	}
}

func TestSanitiseCoursesEmptyIsEmptyNotNil(t *testing.T) {
	// The console iterates this; a nil would serialise as null and break the
	// map on the other side.
	f := Sanitise(&Form{})
	if f.Courses == nil {
		t.Error("courses should be an empty list, not nil")
	}
}

func TestSanitiseYesNo(t *testing.T) {
	for in, want := range map[string]string{
		"Yes": "yes", "NO": "no", "ใช่": "yes", "ไม่ใช่": "no",
		"maybe": "", "": "",
	} {
		got := Sanitise(&Form{EnrolledBefore: Field{Value: in, Confidence: 0.5}}).EnrolledBefore.Value
		if got != want {
			t.Errorf("enrolledBefore %q -> %q, want %q", in, got, want)
		}
	}
}

func TestNewGeminiWithoutKeyIsOff(t *testing.T) {
	// A deployment with no key has no scanning, rather than a provider that
	// fails on every call.
	if p := NewGemini("  ", ""); p != nil {
		t.Errorf("a blank key should yield no provider, got %v", p.Name())
	}
}

func TestNewFromConfig(t *testing.T) {
	if p := New(Config{Provider: "gemini", APIKey: "k"}); p == nil {
		t.Error("gemini with a key should build a provider")
	} else if p.Name() != "gemini/"+defaultGeminiModel {
		t.Errorf("unexpected provider name %q", p.Name())
	}
	if p := New(Config{Provider: ""}); p != nil {
		t.Error("no provider configured should be nil, not a broken provider")
	}
}

/* An identity document, which the form reader's rules do not cover. */

func TestIDCardConvertsABuddhistEraYear(t *testing.T) {
	// A Thai card prints 2540 where the entry form means 1997. The prompt asks
	// for the conversion; a model that answers in BE anyway would otherwise
	// produce a child aged minus five hundred, and every age check would refuse
	// the entry for a reason nobody could act on.
	got := SanitiseIDCard(&IDCard{
		DateOfBirth: Field{Value: "2540-05-02", Confidence: 0.9},
	})
	if got.DateOfBirth.Value != "1997-05-02" {
		t.Fatalf("dateOfBirth = %q, want 1997-05-02", got.DateOfBirth.Value)
	}
	// Converting is not a reason to doubt the characters that were read.
	if got.DateOfBirth.Confidence != 0.9 {
		t.Errorf("confidence = %v, want it untouched", got.DateOfBirth.Confidence)
	}
}

func TestIDCardLeavesACommonEraYearAlone(t *testing.T) {
	// The ordinary case once the model has done what it was asked.
	for _, in := range []string{"1997-05-02", "2016-01-31", "2018-12-01"} {
		got := SanitiseIDCard(&IDCard{DateOfBirth: Field{Value: in, Confidence: 1}})
		if got.DateOfBirth.Value != in {
			t.Errorf("%s became %s", in, got.DateOfBirth.Value)
		}
	}
}

func TestIDCardDistrustsADateItCannotParse(t *testing.T) {
	// Kept, so the entrant can see and correct it — but not shown as read.
	got := SanitiseIDCard(&IDCard{
		DateOfBirth: Field{Value: "02/05/2540", Confidence: 0.8},
	})
	if got.DateOfBirth.Value != "02/05/2540" {
		t.Errorf("an unparseable date should be kept, got %q", got.DateOfBirth.Value)
	}
	if got.DateOfBirth.Confidence != 0 {
		t.Errorf("confidence = %v, want 0", got.DateOfBirth.Confidence)
	}
}

func TestIDCardOnlyClaimsADocumentTypeItKnows(t *testing.T) {
	for in, want := range map[string]string{
		"thai-id": "thai-id", "Thai ID": "thai-id", "passport": "passport",
		"PASSPORT": "passport", "driving licence": "", "": "",
	} {
		got := SanitiseIDCard(&IDCard{DocumentType: in})
		if got.DocumentType != want {
			t.Errorf("documentType %q = %q, want %q", in, got.DocumentType, want)
		}
	}
}

func TestIDCardDoesNotReportConfidenceInNothing(t *testing.T) {
	got := SanitiseIDCard(&IDCard{
		FirstName: Field{Value: "   ", Confidence: 0.95},
		LastName:  Field{Value: " Jaidee ", Confidence: 1.4},
	})
	if got.FirstName.Confidence != 0 {
		t.Errorf("an empty answer is not a confident reading of nothing: %v", got.FirstName)
	}
	if got.LastName.Value != "Jaidee" || got.LastName.Confidence != 1 {
		t.Errorf("lastName = %+v, want trimmed and clamped", got.LastName)
	}
}
