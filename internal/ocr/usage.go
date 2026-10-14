package ocr

// What each call to the provider used, so the console can say what scanning
// costs. Google's reply carries the token counts; the provider hands them to
// whoever asked, and the API keeps them (0077). Nothing read off the document
// is in a Usage.

// Usage is one answered call: which model, what for, and its tokens. Output
// includes thinking, which Google bills as output.
type Usage struct {
	Model        string
	Kind         string // "form", "id_card" or "test"
	InputTokens  int
	OutputTokens int
}

// UsageReporter is a provider that can report each call's usage. Optional:
// a provider without it simply has no cost line.
type UsageReporter interface {
	// ReportUsage returns a copy of the provider that calls report after each
	// call the provider answered. Copies made from it with WithModel report too.
	ReportUsage(report func(Usage)) Provider
}
