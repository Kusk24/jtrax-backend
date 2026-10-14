// What scanning costs: every call Google answered is kept with its tokens
// (0077), and Settings is told this month's total at each model's price on the
// day of the call, and how big an average scan is, so the picker can say what
// a thousand scans would cost on each model.
//
// Prices are ocr's table, copied from Google's pricing page. The total is an
// estimate of the Gemini bill, not the bill: Google's own console is the
// record.
package api

import (
	"database/sql"
	"log"
	"time"

	"github.com/Kusk24/jtrax-backend/internal/academytime"
	"github.com/Kusk24/jtrax-backend/internal/ocr"
)

// How far back an average scan is measured. Long enough to have scans in a
// quiet month, short enough to follow a change of model or prompt.
const scanSizeWindow = 90 * 24 * time.Hour

// recordScanUsage hands back the provider wired to keep each call's tokens.
// A provider that cannot report usage is returned as it was.
func recordScanUsage(d *sql.DB, p ocr.Provider) ocr.Provider {
	reporter, ok := p.(ocr.UsageReporter)
	if !ok {
		return p
	}
	return reporter.ReportUsage(func(u ocr.Usage) {
		// A lost row is a cost line slightly low, never a failed scan.
		if _, err := d.Exec(`INSERT INTO ocr_usage (usage_id, model, kind, input_tokens, output_tokens)
		                     VALUES (?,?,?,?,?)`, newID("ocu"), u.Model, u.Kind, u.InputTokens, u.OutputTokens); err != nil {
			log.Printf("ocr usage: %v", err)
		}
	})
}

// scanCost is the cost block of Settings' scan state: this month (academy
// time) so far, and the average size of a scan.
func scanCost(d *sql.DB) map[string]any {
	now := academytime.Now()
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, academytime.Location())
	out := map[string]any{
		"since": monthStart.Format("2006-01-02"),
		"scans": 0, "tests": 0, "inputTokens": 0, "outputTokens": 0,
		"costUSD": 0.0, "unpricedCalls": 0,
		"avgScanInputTokens": 0, "avgScanOutputTokens": 0,
	}

	// Grouped by model and day: a price can change from one day to the next.
	rows, err := d.Query(`SELECT model, kind, date(created_at), COUNT(*), SUM(input_tokens), SUM(output_tokens)
	                        FROM ocr_usage WHERE created_at >= ?
	                       GROUP BY model, kind, date(created_at)`,
		monthStart.UTC().Format("2006-01-02 15:04:05"))
	if err != nil {
		log.Printf("ocr usage: %v", err)
		return out
	}
	defer rows.Close()
	var scans, tests, unpriced, in, outTokens int
	var cost float64
	for rows.Next() {
		var model, kind, day string
		var calls, input, output int
		if rows.Scan(&model, &kind, &day, &calls, &input, &output) != nil {
			continue
		}
		if kind == "test" {
			tests += calls
		} else {
			scans += calls
		}
		in, outTokens = in+input, outTokens+output
		when, _ := time.Parse("2006-01-02", day)
		if price, _, ok := ocr.PriceOn(model, when); ok {
			cost += (float64(input)*price.InputPerMillion + float64(output)*price.OutputPerMillion) / 1e6
		} else {
			unpriced += calls
		}
	}
	out["scans"], out["tests"], out["unpricedCalls"] = scans, tests, unpriced
	out["inputTokens"], out["outputTokens"], out["costUSD"] = in, outTokens, cost

	var avgIn, avgOut sql.NullFloat64
	d.QueryRow(`SELECT AVG(input_tokens), AVG(output_tokens) FROM ocr_usage
	             WHERE kind != 'test' AND created_at >= ?`,
		time.Now().Add(-scanSizeWindow).UTC().Format("2006-01-02 15:04:05")).Scan(&avgIn, &avgOut)
	out["avgScanInputTokens"], out["avgScanOutputTokens"] = int(avgIn.Float64+0.5), int(avgOut.Float64+0.5)
	return out
}
