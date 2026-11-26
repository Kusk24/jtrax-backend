// Tournament pricing and fee calculations.
package fees

import (
	"database/sql"
	"math"
)

// TournamentPrice is everything about a tournament that bears on its fee.
type TournamentPrice struct {
	Regular        float64
	EarlyBird      float64
	EarlyBirdUntil string
	DiscountPct    int
	// Which of the two reductions a JCA student is offered. Chosen by the
	// organiser per event (0035); the defaults are the discount alone.
	StudentDiscount  bool
	StudentEarlyBird bool
}

// priceSelect reads a tournamentPrice; `scanPrice` is its other half. The
// COALESCE on the regular fee mirrors the public page: an event with only an
// early-bird price charges that.
const priceSelect = `
	SELECT COALESCE(regular_fee, early_bird_fee, 0), COALESCE(early_bird_fee, 0),
	       COALESCE(early_bird_deadline, ''), student_discount_pct,
	       student_gets_discount, student_gets_early_bird
	  FROM tournament`

func scanPrice(sc interface{ Scan(...any) error }) (TournamentPrice, error) {
	var p TournamentPrice
	err := sc.Scan(&p.Regular, &p.EarlyBird, &p.EarlyBirdUntil, &p.DiscountPct,
		&p.StudentDiscount, &p.StudentEarlyBird)
	return p, err
}

// LoadPrice reads one tournament's pricing; sql.ErrNoRows when it is missing.
func LoadPrice(q interface {
	QueryRow(string, ...any) *sql.Row
}, tournamentID string) (TournamentPrice, error) {
	return scanPrice(q.QueryRow(priceSelect+` WHERE tournament_id = ?`, tournamentID))
}

// EarlyBirdOpen reports whether the early-bird window runs on `today`. A price
// with no end date was never chargeable (see 0029), so it is not open.
func (p TournamentPrice) EarlyBirdOpen(today string) bool {
	return p.EarlyBird > 0 && p.EarlyBirdUntil != "" && today <= p.EarlyBirdUntil
}

// OutsiderFee is what somebody who is not a JCA student pays: the early-bird
// price while its window is open, the regular price after.
func (p TournamentPrice) OutsiderFee(today string) float64 {
	if p.EarlyBirdOpen(today) {
		return p.EarlyBird
	}
	return p.Regular
}

// StudentFee is what a JCA student pays, by whichever reductions the organiser
// switched on. With both, the discount comes off the early-bird price — that is
// what "both" means to a parent reading the page.
func (p TournamentPrice) StudentFee(today string) float64 {
	fee := p.Regular
	if p.StudentEarlyBird && p.EarlyBirdOpen(today) {
		fee = p.EarlyBird
	}
	if p.StudentDiscount {
		fee = discounted(fee, p.DiscountPct)
	}
	return fee
}

// StudentDiscountApplied reports whether a student's fee today includes the
// percentage discount — recorded on the registration beside the fee.
func (p TournamentPrice) StudentDiscountApplied() bool {
	return p.StudentDiscount && p.DiscountPct > 0
}

// discounted applies a percentage off, rounded to the nearest whole unit of
// currency. Fees here are whole baht; half a baht is not a price anybody quotes.
func discounted(fee float64, pct int) float64 {
	if pct <= 0 || fee <= 0 {
		return fee
	}
	return math.Round(fee * float64(100-pct) / 100)
}
