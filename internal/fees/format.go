// Tournament pricing and fee calculations.
package fees

import (
	"math"
	"strconv"
)

// FmtBaht writes an amount with a thousands separator and the ISO code —
// "12,000 THB". Whole satang are the norm at the till, so decimals only
// appear when the amount has them.
func FmtBaht(v float64) string {
	whole := int64(math.Trunc(math.Abs(v)))
	frac := math.Round((math.Abs(v)-float64(whole))*100) / 100
	digits := strconv.FormatInt(whole, 10)
	var out []byte
	for i, c := range []byte(digits) {
		if i > 0 && (len(digits)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, c)
	}
	text := string(out)
	if frac > 0 {
		text += strconv.FormatFloat(frac, 'f', 2, 64)[1:]
	}
	if v < 0 {
		text = "-" + text
	}
	return text + " THB"
}
