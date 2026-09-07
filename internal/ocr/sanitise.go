package ocr

import (
	"strings"
	"time"
)

// Courses are the three boxes printed on the form. Anything else the model
// offers is dropped rather than passed through: the console renders these as
// known options, and an invented fourth course would be a silent bad row.
var Courses = []string{"CHESS", "CODING", "ART & DESIGN"}

// Sanitise makes a provider's reply safe to hand to the console. Every rule
// here exists because a model can return it: confidence outside 0..1, a course
// that is not on the form, whitespace around a name, a date in a shape no date
// input will accept.
//
// It never discards a value the staff member could fix by hand — a date it
// cannot parse keeps its text and loses its confidence, so the console flags it
// for attention instead of silently blanking what the parent wrote.
func Sanitise(f *Form) *Form {
	if f == nil {
		return nil
	}
	for _, p := range []*Field{
		&f.Name, &f.Gender, &f.Address, &f.DateOfBirth, &f.Email,
		&f.CurrentSchool, &f.ContactNumber, &f.ChessLevel, &f.FideID,
		&f.FideRating, &f.HowDidYouKnow, &f.EnrolledBefore,
		&f.PreviousSchool, &f.CoursePackage,
	} {
		clean(p)
	}

	f.ContactNumber.Value = keepPhoneChars(f.ContactNumber.Value)
	f.EnrolledBefore.Value = normaliseYesNo(f.EnrolledBefore.Value)

	// A date the console cannot put in a date input is still worth showing, but
	// must not look confident.
	if f.DateOfBirth.Value != "" && !isISODate(f.DateOfBirth.Value) {
		f.DateOfBirth.Confidence = 0
	}

	f.Courses = keepKnownCourses(f.Courses)
	return f
}

func clean(f *Field) {
	f.Value = strings.TrimSpace(f.Value)
	switch {
	case f.Value == "":
		// An empty answer is not a confident reading of nothing.
		f.Confidence = 0
	case f.Confidence < 0:
		f.Confidence = 0
	case f.Confidence > 1:
		f.Confidence = 1
	}
}

func keepPhoneChars(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= '0' && r <= '9') || r == '+' || r == ' ' || r == '-' {
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
}

func normaliseYesNo(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "yes", "y", "true", "ใช่":
		return "yes"
	case "no", "n", "false", "ไม่", "ไม่ใช่":
		return "no"
	}
	return ""
}

func isISODate(s string) bool {
	_, err := time.Parse("2006-01-02", s)
	return err == nil
}

// keepKnownCourses uppercases, drops anything not printed on the form, and
// removes duplicates while keeping the form's own order.
func keepKnownCourses(in []string) []string {
	seen := map[string]bool{}
	for _, c := range in {
		seen[strings.ToUpper(strings.TrimSpace(c))] = true
	}
	out := []string{}
	for _, known := range Courses {
		if seen[known] {
			out = append(out, known)
		}
	}
	return out
}
