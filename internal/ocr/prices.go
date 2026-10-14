package ocr

// What each Gemini model costs, so Settings can show the price beside the model.
//
// Google's model list says nothing about price, and a Gemini key has no price
// API, so these are copied from the pricing page, with the date they were
// copied. A change Google has already announced is written in with the day it
// starts, so the price shown moves on that day without a redeploy.

import (
	"sort"
	"time"
)

// Where the prices come from, and when they were last copied. Shown in
// Settings so a stale table is visible as one.
const (
	PricesSource = "https://ai.google.dev/gemini-api/docs/pricing"
	PricesAsOf   = "2026-10-13"
)

// Price is a model's Standard-tier paid price in US dollars per million
// tokens. A photo is billed as input tokens; output includes the model's
// thinking. From is the first day it applies, empty when it already did on
// PricesAsOf.
type Price struct {
	From             string  `json:"from,omitempty"`
	InputPerMillion  float64 `json:"inputPerMillion"`
	OutputPerMillion float64 `json:"outputPerMillion"`
}

// The models that can read a photo, at the price for a prompt under 200k
// tokens (a scan is a few thousand). A model missing here is shown without a
// price rather than with a guess.
var prices = map[string][]Price{
	"gemini-3.8-flash":          {{"", 0.75, 3.75}, {"2027-01-01", 1.50, 7.50}},
	"gemini-3.6-flash":          {{"", 0.75, 3.75}, {"2027-01-01", 1.50, 7.50}},
	"gemini-3.5-flash-lite":     {{"", 0.30, 2.50}},
	"gemini-3.1-flash-lite":     {{"", 0.25, 1.50}},
	"gemini-3-flash-preview":    {{"", 0.50, 3.00}},
	"gemini-3.1-pro-preview":    {{"", 2.00, 12.00}},
	"gemini-4-argon":            {{"", 2.00, 10.00}, {"2026-12-01", 4.00, 20.00}},
	"gemini-omni-1.1-flash":     {{"", 1.50, 9.00}},
	"gemini-omni-flash-preview": {{"", 1.50, 9.00}},
	"gemini-2.5-pro":            {{"", 1.25, 10.00}},
	"gemini-2.5-flash":          {{"", 0.30, 2.50}},
	"gemini-2.5-flash-lite":     {{"", 0.10, 0.40}},
}

// PriceOn is what model costs on day, and the next price Google has announced
// after it, if any. ok is false for a model with no price on file.
func PriceOn(model string, day time.Time) (now Price, next *Price, ok bool) {
	steps := prices[model]
	if len(steps) == 0 {
		return Price{}, nil, false
	}
	sorted := append([]Price(nil), steps...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].From < sorted[j].From })
	date := day.Format("2006-01-02")
	for i, p := range sorted {
		if p.From != "" && p.From > date {
			if i == 0 {
				// Every price on file starts later: nothing applies yet.
				return Price{}, nil, false
			}
			upcoming := p
			return sorted[i-1], &upcoming, true
		}
	}
	return sorted[len(sorted)-1], nil, true
}
