// Credit reminders and notifications.
package payments

import (
	"database/sql"
	"math"
	"strconv"
)

// lowCreditLine is the academy's own threshold, the same
// `credit_rule_low_credit` the console's Settings screen edits. Default 3,
// matching the console's.
func lowCreditLine(d *sql.DB) float64 {
	var raw string
	if err := d.QueryRow(
		`SELECT config_value FROM system_configuration WHERE config_key = 'credit_rule_low_credit'`,
	).Scan(&raw); err == nil {
		if v, err := strconv.ParseFloat(raw, 64); err == nil && v >= 0 {
			return v
		}
	}
	return 3
}

// FmtCreditsShort writes a credit count the way a person would: whole numbers
// bare, fractions to at most two places — 13.499999999999998 is the right
// number and the wrong thing to put in a sentence.
func FmtCreditsShort(v float64) string {
	r := math.Round(v*100) / 100
	if r == math.Trunc(r) {
		return strconv.FormatInt(int64(r), 10)
	}
	return strconv.FormatFloat(r, 'f', -1, 64)
}
