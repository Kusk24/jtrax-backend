package ocr

import (
	"context"
	"encoding/json"
	"testing"
)

// The reply Google sends for a scan, with its token counts.
func replyWithUsage(inner string, prompt, candidates, thoughts int) string {
	out := map[string]any{
		"candidates": []any{map[string]any{
			"content": map[string]any{"parts": []any{map[string]any{"text": inner}}},
		}},
		"usageMetadata": map[string]any{
			"promptTokenCount": prompt, "candidatesTokenCount": candidates, "thoughtsTokenCount": thoughts,
		},
	}
	b, _ := json.Marshal(out)
	return string(b)
}

// Each answered call is reported with its tokens; thinking counts as output,
// because Google bills it as output.
func TestEveryAnsweredCallReportsItsTokens(t *testing.T) {
	g, _, _ := stubGemini(t, replyWithUsage(`{"name":{"value":"A","confidence":1}}`, 1800, 250, 400), 200)
	var got []Usage
	p := g.ReportUsage(func(u Usage) { got = append(got, u) }).(*gemini)

	if _, err := p.Extract(context.Background(), []byte{1}, "image/jpeg"); err != nil {
		t.Fatal(err)
	}
	if err := p.Test(context.Background()); err != nil {
		t.Fatal(err)
	}
	// A model switched in Settings keeps reporting.
	if _, err := p.WithModel("gemini-3.6-flash").(*gemini).ExtractIDCard(context.Background(), []byte{1}, "image/jpeg"); err != nil {
		t.Fatal(err)
	}

	want := []Usage{
		{Model: defaultGeminiModel, Kind: "form", InputTokens: 1800, OutputTokens: 650},
		{Model: defaultGeminiModel, Kind: "test", InputTokens: 1800, OutputTokens: 650},
		{Model: "gemini-3.6-flash", Kind: "id_card", InputTokens: 1800, OutputTokens: 650},
	}
	if len(got) != len(want) {
		t.Fatalf("reports = %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("report %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// A call Google refused is not billed, so it is not reported.
func TestARefusedCallReportsNothing(t *testing.T) {
	g, _, _ := stubGemini(t, `{"error":{}}`, 503)
	reported := 0
	p := g.ReportUsage(func(Usage) { reported++ }).(*gemini)
	p.Extract(context.Background(), []byte{1}, "image/jpeg")
	if reported != 0 {
		t.Errorf("a refused call must not be counted, got %d reports", reported)
	}
}
