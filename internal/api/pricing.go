// What a tournament entry costs, decided in one place.
//
// The price used to be worked out twice — once for the public form and once,
// by the browser, for the parent portal — and the portal's copy was the one
// written to the registration. A parent could therefore send any fee they
// liked. The server now prices every entry itself, from the tournament the
// organiser set up, and this file is the only rule it uses.
package api

import (
	"database/sql"
	"math"
)

// tournamentPrice is everything about a tournament that bears on its fee.
type tournamentPrice struct {
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

func scanPrice(sc interface{ Scan(...any) error }) (tournamentPrice, error) {
	var p tournamentPrice
	err := sc.Scan(&p.Regular, &p.EarlyBird, &p.EarlyBirdUntil, &p.DiscountPct,
		&p.StudentDiscount, &p.StudentEarlyBird)
	return p, err
}

// loadPrice reads one tournament's pricing; sql.ErrNoRows when it is missing.
func loadPrice(q interface {
	QueryRow(string, ...any) *sql.Row
}, tournamentID string) (tournamentPrice, error) {
	return scanPrice(q.QueryRow(priceSelect+` WHERE tournament_id = ?`, tournamentID))
}

// earlyBirdOpen reports whether the early-bird window runs on `today`. A price
// with no end date was never chargeable (see 0029), so it is not open.
func (p tournamentPrice) earlyBirdOpen(today string) bool {
	return p.EarlyBird > 0 && p.EarlyBirdUntil != "" && today <= p.EarlyBirdUntil
}

// OutsiderFee is what somebody who is not a JCA student pays: the early-bird
// price while its window is open, the regular price after.
func (p tournamentPrice) OutsiderFee(today string) float64 {
	if p.earlyBirdOpen(today) {
		return p.EarlyBird
	}
	return p.Regular
}

// StudentFee is what a JCA student pays, by whichever reductions the organiser
// switched on. With both, the discount comes off the early-bird price — that is
// what "both" means to a parent reading the page.
func (p tournamentPrice) StudentFee(today string) float64 {
	fee := p.Regular
	if p.StudentEarlyBird && p.earlyBirdOpen(today) {
		fee = p.EarlyBird
	}
	if p.StudentDiscount {
		fee = discounted(fee, p.DiscountPct)
	}
	return fee
}

// StudentDiscountApplied reports whether a student's fee today includes the
// percentage discount — recorded on the registration beside the fee.
func (p tournamentPrice) StudentDiscountApplied() bool {
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

// withStudentFee adds `student_fee` to a tournament row as the API returns it,
// so a portal shows the price it will be charged rather than working one out.
func withStudentFee(row map[string]any) {
	p := tournamentPrice{
		Regular:          num(row["regular_fee"]),
		EarlyBird:        num(row["early_bird_fee"]),
		DiscountPct:      int(num(row["student_discount_pct"])),
		StudentDiscount:  num(row["student_gets_discount"]) != 0,
		StudentEarlyBird: num(row["student_gets_early_bird"]) != 0,
	}
	if p.Regular == 0 {
		p.Regular = p.EarlyBird
	}
	p.EarlyBirdUntil, _ = row["early_bird_deadline"].(string)
	row["student_fee"] = p.StudentFee(todayISO())
}

// num reads a number out of a scanned row, which holds int64 or float64
// depending on how SQLite stored it, and nil for NULL.
func num(v any) float64 {
	switch n := v.(type) {
	case int64:
		return float64(n)
	case float64:
		return n
	}
	return 0
}
