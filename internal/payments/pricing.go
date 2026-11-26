// Credit reminders and notifications.
package payments

import (
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"strings"

	"github.com/Kusk24/jtrax-backend/internal/academytime"
	"github.com/Kusk24/jtrax-backend/internal/fees"
)

// WithStudentFee adds `student_fee` to a tournament row as the API returns it,
// so a portal shows the price it will be charged rather than working one out.
func WithStudentFee(row map[string]any) {
	p := fees.TournamentPrice{
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
	row["student_fee"] = p.StudentFee(academytime.Today())
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

// CheckPackageValidity lets a credit package's validity be blank — never
// expires — or a whole number of days from 1. Zero used to be accepted as a
// second way of writing "never", which read as "expires at once" to anybody
// who saw it; it is refused now, and 0061 cleared the ones already stored.
func CheckPackageValidity(row map[string]any) error {
	v, present := row["validity_days"]
	if !present || v == nil || v == "" {
		return nil
	}
	var days float64
	switch n := v.(type) {
	case float64:
		days = n
	case int64:
		days = float64(n)
	case int:
		days = float64(n)
	case json.Number:
		days, _ = n.Float64()
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(n), 64)
		if err != nil {
			return errors.New("validity must be a number of days, or blank for never expires")
		}
		days = f
	default:
		return errors.New("validity must be a number of days, or blank for never expires")
	}
	if days < 1 || days != math.Trunc(days) {
		return errors.New("validity must be at least 1 day — leave it blank for credits that never expire")
	}
	return nil
}
