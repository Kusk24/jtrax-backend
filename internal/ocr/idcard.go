package ocr

import "context"

// Reading a player's Thai ID card or passport, to fill in the two fields a
// tournament entry form asks for and a card already answers: the name, and how
// old the player is.
//
// # Why this is not the registration-form reader
//
// That one reads handwriting off a printed form we designed. This one reads
// print off a document somebody else designed, in two layouts (Thai national ID
// and passport), where the fields are in fixed places and the failure modes are
// different — a smeared handwritten 7 versus a glare-blown line of MRZ.
//
// The result shape is different too, and deliberately smaller. A registration
// form is a thing the academy keeps; an ID card is a thing it *checks*. The only
// values wanted are the ones the entry form would otherwise make a parent type
// twice, so the address, the ID number and everything else on the card is not
// asked for and not returned.
//
// # What is not kept
//
// The image is never written to the database. It is read, the fields come back,
// and the bytes go out of scope with the request — see the entry handler. A
// store of children's identity documents is a thing to leak, and a thing
// somebody would later have to be asked to delete.
//
// Nothing here is authoritative either. Every value carries a confidence and
// lands in a form field the entrant can correct before submitting: a misread
// date of birth that silently changed which age group a child entered would be
// worse than no scan at all.

// IDCard is what a tournament entry needs off an identity document.
type IDCard struct {
	// The name as printed. Split because the entry form asks for the two
	// separately, and splitting on the last space is wrong for Thai names.
	FirstName Field `json:"firstName"`
	LastName  Field `json:"lastName"`
	// Normalised to YYYY-MM-DD. The age is worked out from this rather than
	// read, because a card prints a date and never an age.
	DateOfBirth Field `json:"dateOfBirth"`
	// "thai-id", "passport", or "" when the document is neither or unclear.
	// Reported so the console can say what it thinks it read, not to gate
	// anything: an entrant holding a document we did not classify still enters.
	DocumentType string `json:"documentType"`
}

// IDCardReader is implemented by providers that can also read an ID card.
//
// Separate from Provider rather than added to it: a provider that reads the
// academy's own form is not automatically one we would point at a passport, and
// the console asks for the capability rather than assuming it.
type IDCardReader interface {
	ExtractIDCard(ctx context.Context, image []byte, mime string) (*IDCard, error)
}
