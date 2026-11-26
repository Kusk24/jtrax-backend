// Email content and entry facts for tournament families.
package entrymail

import (
	"database/sql"
	"log"

	"github.com/Kusk24/jtrax-backend/internal/academytime"
	"github.com/Kusk24/jtrax-backend/internal/entrylinks"
	"github.com/Kusk24/jtrax-backend/internal/fees"
	"github.com/Kusk24/jtrax-backend/internal/mail"
	"github.com/Kusk24/jtrax-backend/internal/notify"
)

type entryNotice int

const (
	NoticeConfirmed entryNotice = iota
	NoticeReserved
	NoticePaymentFailed
	NoticeReminder
	NoticeCancelled
)

// entryFacts is one entry as its emails and its pay page describe it.
type entryFacts struct {
	RegID, TournamentID, TournamentName, Participant, Category string
	Email, Source, Status                                      string
	StartDate, EndDate, VenueName, VenueAddress, Deadline      string
	// Fee is what the entry costs today: fee_charged, already moved to the
	// regular price if the early-bird date passed unpaid (entryrules.go).
	Fee              float64
	EarlyBirdApplied bool
	EarlyBirdUntil   string
	PricedAsStudent  bool
	StudentID        sql.NullString
	PaymentID        sql.NullString
	// PaymentStatus is "Pending", "Paid", "Cancelled", or "" when
	// the entry has no payment (nothing to pay, or an entry from before 0059).
	PaymentStatus string
	PaidAmount    float64
}

func LoadEntryFacts(d *sql.DB, regID string) (*entryFacts, error) {
	f := entryFacts{RegID: regID}
	var paid sql.NullFloat64
	var payStatus sql.NullString
	var earlyBird, student sql.NullInt64
	err := d.QueryRow(`
		SELECT r.tournament_id, t.name, r.participant_name, COALESCE(c.name, ''),
		       r.contact_email, r.source, r.status,
		       COALESCE(t.start_date, ''), COALESCE(t.end_date, ''),
		       COALESCE(t.venue_name, ''), COALESCE(t.venue_address, ''),
		       COALESCE(t.registration_deadline, ''),
		       COALESCE(r.fee_charged, r.fee_quoted, 0), r.early_bird_applied,
		       COALESCE(t.early_bird_deadline, ''), r.priced_as_student, r.student_id,
		       p.payment_id, p.status, p.final_amount
		  FROM tournament_registration r
		  JOIN tournament t ON t.tournament_id = r.tournament_id
		  LEFT JOIN tournament_category c ON c.tournament_category_id = r.tournament_category_id
		  LEFT JOIN payment p ON p.tournament_registration_id = r.tournament_registration_id AND p.status <> 'Voided'
		 WHERE r.tournament_registration_id = ?`, regID).
		Scan(&f.TournamentID, &f.TournamentName, &f.Participant, &f.Category,
			&f.Email, &f.Source, &f.Status, &f.StartDate, &f.EndDate,
			&f.VenueName, &f.VenueAddress, &f.Deadline,
			&f.Fee, &earlyBird, &f.EarlyBirdUntil, &student, &f.StudentID,
			&f.PaymentID, &payStatus, &paid)
	if err != nil {
		return nil, err
	}
	f.EarlyBirdApplied = earlyBird.Int64 == 1
	f.PricedAsStudent = student.Int64 == 1
	f.PaymentStatus = payStatus.String
	f.PaidAmount = paid.Float64
	return &f, nil
}

// EarlyBirdHolds reports whether the entry still costs the early-bird price
// on `day` — it was given it, and the date has not passed.
func (f *entryFacts) EarlyBirdHolds(day string) bool {
	return f.EarlyBirdApplied && f.EarlyBirdUntil != "" && day <= f.EarlyBirdUntil
}

// RegularFee is what the entry will cost once its early-bird date passes,
// by the same rule lapseEarlyBird reprices it with.
func (f *entryFacts) RegularFee(d *sql.DB) float64 {
	price, err := fees.LoadPrice(d, f.TournamentID)
	if err != nil {
		return f.Fee
	}
	price.EarlyBirdUntil = ""
	if f.PricedAsStudent {
		return price.StudentFee(academytime.Today())
	}
	return price.OutsiderFee(academytime.Today())
}

func (f *entryFacts) when() string {
	switch {
	case f.StartDate == "":
		return ""
	case f.EndDate == "" || f.EndDate == f.StartDate:
		return LongDate(f.StartDate)
	default:
		return LongDate(f.StartDate) + " – " + LongDate(f.EndDate)
	}
}

func (f *entryFacts) Venue() string {
	switch {
	case f.VenueName != "" && f.VenueAddress != "":
		return f.VenueName + ", " + f.VenueAddress
	case f.VenueName != "":
		return f.VenueName
	}
	return f.VenueAddress
}

// entryEmail writes one notice. link is the entry's pay page, "" when there
// is no portal address to build it from; online says whether the page can
// take a card, which decides how the email says to pay.
func entryEmail(d *sql.DB, f *entryFacts, kind entryNotice, link string, online bool) (string, mail.Email) {
	day := academytime.Today()
	details := []mail.Detail{{Label: "Player", Value: f.Participant}, {Label: "Tournament", Value: f.TournamentName}}
	if w := f.when(); w != "" {
		details = append(details, mail.Detail{Label: "Date", Value: w})
	}
	if v := f.Venue(); v != "" {
		details = append(details, mail.Detail{Label: "Venue", Value: v})
	}
	if f.Category != "" {
		details = append(details, mail.Detail{Label: "Category", Value: f.Category})
	}
	// The fee as it stands today: early-bird with its date while that holds,
	// and the price after it, so nobody is surprised by the change.
	owed := func() {
		if f.EarlyBirdHolds(day) {
			details = append(details,
				mail.Detail{Label: "Early-bird fee", Value: fees.FmtBaht(f.Fee) + " — pay by " + LongDate(f.EarlyBirdUntil)},
				mail.Detail{Label: "Regular fee", Value: fees.FmtBaht(f.RegularFee(d)) + " after " + LongDate(f.EarlyBirdUntil)})
		} else {
			details = append(details, mail.Detail{Label: "Entry fee", Value: fees.FmtBaht(f.Fee)})
		}
		if f.Deadline != "" {
			details = append(details, mail.Detail{Label: "Registration closes", Value: LongDate(f.Deadline)})
		}
	}
	howToPay := "Please pay by bank transfer or at the JCA front desk before registration closes."
	howToPayTH := "กรุณาโอนเงินผ่านธนาคาร หรือชำระที่เคาน์เตอร์ของ JCA ก่อนวันปิดรับสมัคร"
	var button *mail.Button
	if online && link != "" {
		howToPay = "You can pay online by card or PromptPay with the button below, or at the JCA front desk before registration closes."
		howToPayTH = "ชำระออนไลน์ด้วยบัตรหรือพร้อมเพย์ได้จากปุ่มด้านล่าง หรือชำระที่เคาน์เตอร์ของ JCA ก่อนวันปิดรับสมัคร"
		button = &mail.Button{Label: "Complete Payment", URL: link}
	}
	released := "If payment has not been received by the time registration closes, the reserved place will be released."
	releasedTH := "หากยังไม่ได้รับชำระเงินภายในวันปิดรับสมัคร สิทธิ์การแข่งขันที่จองไว้จะถูกยกเลิก"
	// The footer every email carries gives the contact details in English;
	// the Thai line stays here.
	_, note := contactLines()

	t, p := f.TournamentName, f.Participant
	switch kind {
	case NoticeConfirmed:
		paras := []string{p + " has been successfully registered for " + t + "."}
		thai := p + " ลงทะเบียนเข้าร่วม " + t + " เรียบร้อยแล้ว"
		if f.PaymentStatus == "Paid" {
			details = append(details,
				mail.Detail{Label: "Payment status", Value: "Paid"},
				mail.Detail{Label: "Amount paid", Value: fees.FmtBaht(f.PaidAmount)})
			paras = append(paras, "Your registration and payment have been completed.")
			thai += " และได้รับชำระค่าสมัครแล้ว"
		}
		if link != "" {
			button = &mail.Button{Label: "View Registration", URL: link}
		} else {
			button = nil
		}
		return "Registration Confirmed — " + p + " — " + t, mail.Email{
			Heading: "Registration confirmed", Greeting: "Hello,",
			Paragraphs: append(paras, thai), Details: details, Button: button, Note: note,
		}

	case NoticeReserved, NoticePaymentFailed:
		why := "You chose to pay later. Your place has been reserved, but payment has not yet been completed."
		whyTH := "คุณเลือกชำระเงินภายหลัง ที่นั่งของคุณถูกจองไว้แล้ว แต่ยังไม่ได้ชำระเงิน"
		if kind == NoticePaymentFailed {
			why = "We were unable to complete your online payment. Your place has been reserved, but payment is still required."
			whyTH = "การชำระเงินออนไลน์ของคุณยังไม่สำเร็จ ที่นั่งของคุณถูกจองไว้แล้ว แต่ยังต้องชำระเงิน"
		}
		details = append(details, mail.Detail{Label: "Payment status", Value: "Pending"})
		owed()
		return "Registration Reserved — Payment Required for " + p + " — " + t, mail.Email{
			Heading: "Your place is reserved", Greeting: "Hello,",
			Paragraphs: []string{
				p + " has been successfully registered for " + t + ".",
				why, howToPay, released,
				p + " ลงทะเบียนเข้าร่วม " + t + " เรียบร้อยแล้ว " + whyTH + " " + howToPayTH + " " + releasedTH,
			},
			Details: details, Button: button, Note: note,
		}

	case NoticeReminder:
		closes := LongDate(f.Deadline)
		details = append(details, mail.Detail{Label: "Payment status", Value: "Pending"})
		owed()
		return "Payment Reminder — " + p + " — " + t + " registration closes " + closes, mail.Email{
			Heading: "Payment due tomorrow", Greeting: "Hello,",
			Paragraphs: []string{
				p + "'s place in " + t + " is reserved, but the entry fee has not been paid yet.",
				"Please pay by " + closes + ", when registration closes. If payment has not been received by then, the registration will be cancelled and the place released.",
				howToPay,
				"ที่นั่งของ " + p + " ใน " + t + " ยังรอการชำระเงิน กรุณาชำระภายในวันที่ " + closes +
					" ซึ่งเป็นวันปิดรับสมัคร หากยังไม่ได้รับชำระเงิน การสมัครจะถูกยกเลิกและสิทธิ์การแข่งขันจะถูกปล่อยให้ผู้อื่น " + howToPayTH,
			},
			Details: details, Button: button, Note: note,
		}

	default: // noticeCancelled
		closes := LongDate(f.Deadline)
		details = append(details, mail.Detail{Label: "Payment status", Value: "Cancelled"})
		return "Registration Cancelled — " + p + " — " + t, mail.Email{
			Heading: "Registration cancelled", Greeting: "Hello,",
			Paragraphs: []string{
				"The registration for " + p + " in " + t + " has been cancelled because the entry fee was not paid by " + closes + ", when registration closed.",
				"The place has been released and is no longer reserved.",
				"If you have already paid or believe this is a mistake, please contact JCA Chess School.",
				"การสมัครของ " + p + " ใน " + t + " ถูกยกเลิกแล้ว เนื่องจากไม่ได้ชำระค่าสมัครภายในวันที่ " + closes +
					" ซึ่งเป็นวันปิดรับสมัคร สิทธิ์การแข่งขันถูกปล่อยให้ผู้อื่นแล้ว หากคุณชำระเงินแล้วหรือคิดว่าเกิดข้อผิดพลาด กรุณาติดต่อ JCA Chess School",
			},
			Details: details, Note: note,
		}
	}
}

// SendEntryNotice emails one notice to the address the entrant gave. Only
// entries from the public form: a parent's own entry is told through their
// inbox. Failures are logged, never returned — an email that did not go is
// no reason to undo a payment or a release.
func SendEntryNotice(d *sql.DB, svc *notify.Service, regID string, kind entryNotice, online bool) {
	if svc == nil {
		return
	}
	f, err := LoadEntryFacts(d, regID)
	if err != nil {
		log.Printf("entry email %s: %v", regID, err)
		return
	}
	if f.Source != "Public" || f.Email == "" {
		return
	}
	var link string
	if svc.AppURL() != "" {
		code, err := entrylinks.CurrentPayCode(d, regID)
		if err != nil {
			log.Printf("entry email %s: pay code: %v", regID, err)
		} else {
			link = entrylinks.PayLink(svc.AppURL(), f.TournamentID, regID, code)
		}
	}
	subject, e := entryEmail(d, f, kind, link, online && f.Fee > 0)
	svc.Email(f.Email, subject, e)
}
