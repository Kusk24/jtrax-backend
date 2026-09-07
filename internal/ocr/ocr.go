// Package ocr reads a photographed JCA registration form and returns the
// fields a member of staff would otherwise retype.
//
// The thing that decides the design: the blanks on that form are filled in by
// **hand**. Classic OCR (Tesseract and friends) is excellent on printed text
// and close to useless on handwriting — pointed at this form it reads the
// printed labels we already know and returns noise for the answers. So the
// provider here is a vision model, which also solves the other two awkward
// bits for free: mixed Thai and English handwriting, and telling us which of
// the CHESS / CODING / ART & DESIGN boxes is ticked.
//
// Nothing here is authoritative. Every value comes back with a confidence and
// is handed to a human to correct before anything is saved — a misread date of
// birth is worse than a blank one, and these are children's records.
package ocr

import (
	"context"
	"errors"
)

// ErrNotConfigured is returned when no provider is set up. The caller turns it
// into a 503 so the console can say "scanning is not switched on" rather than
// failing in a way that looks like the photo was bad.
var ErrNotConfigured = errors.New("ocr: no provider configured")

// Field is one value read off the form, with how sure the model was about it.
// Confidence drives which boxes the console asks staff to double-check; it is
// not a threshold for accepting anything automatically, because nothing is
// accepted automatically.
type Field struct {
	Value      string  `json:"value"`
	Confidence float64 `json:"confidence"`
}

// Form mirrors the paper form field for field, including the ones JTrax has
// nowhere to put yet (gender, address, how they heard of the academy). Reading
// them costs nothing extra and means the console can start using them the day
// the schema catches up, without another pass over the model prompt.
type Form struct {
	Name           Field `json:"name"`
	Gender         Field `json:"gender"`
	Address        Field `json:"address"`
	DateOfBirth    Field `json:"dateOfBirth"` // normalised to YYYY-MM-DD
	Email          Field `json:"email"`
	CurrentSchool  Field `json:"currentSchool"`
	ContactNumber  Field `json:"contactNumber"`
	ChessLevel     Field `json:"chessLevel"`
	FideID         Field `json:"fideId"`
	FideRating     Field `json:"fideRating"`
	HowDidYouKnow  Field `json:"howDidYouKnow"`
	EnrolledBefore Field `json:"enrolledBefore"`
	PreviousSchool Field `json:"previousSchool"`
	CoursePackage  Field `json:"coursePackage"`
	// Courses is the ticked subset of CHESS / CODING / ART & DESIGN. A list
	// rather than one value because the form's boxes are not exclusive.
	Courses []string `json:"courses"`
}

// Provider reads one image. Implementations must not retain the image or the
// text they extract beyond the call: the picture is a child's name, address and
// date of birth, and the academy's copy of record is the form itself.
type Provider interface {
	// Extract reads the form. mime is the uploaded image's content type.
	Extract(ctx context.Context, image []byte, mime string) (*Form, error)
	// Name identifies the provider in logs and in the health response, so it is
	// answerable which service saw the data.
	Name() string
}
