// Gemini as the vision provider.
//
// Chosen because it is the only major vision API with a free tier that does not
// require a registered payment method, which is the academy's standing infra
// constraint. Two things to know before switching it on:
//
//   - On the FREE tier Google may use prompts to improve its products and train
//     models. The prompt here is a photograph of a child's name, address, date
//     of birth and phone number. Use a paid key — the same code, the same
//     endpoint, different terms — or run the extraction somewhere else.
//   - The provider is swapped by changing OCR_PROVIDER, not by editing calling
//     code: everything downstream depends on the Provider interface only.
package ocr

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// The model when nothing else picks one. Not gemini-2.5-flash, though it is
// still listed: Google now serves the 2.5 models only to accounts that already
// used them, so a key made today gets a 404 for it and every scan fails.
const defaultGeminiModel = "gemini-3.8-flash"

const geminiEndpoint = "https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent"

// The instruction. It is deliberately explicit about the two mistakes a model
// makes on this form: inventing a plausible value for a blank line, and
// guessing at an ambiguous handwritten date.
const extractPrompt = `You are reading a photograph of a JCA Chess Academy paper registration form.
The labels are printed; the answers are handwritten, and may be in Thai or English.

Return the handwritten answer for each field. Rules:
- If a line is blank or you cannot read it, return an empty string and confidence 0. Never invent a plausible value.
- confidence is 0..1: how sure you are of the characters you read, not how sure you are the field exists.
- dateOfBirth: normalise to YYYY-MM-DD. If the day/month order is ambiguous, still answer but set confidence below 0.5.
- contactNumber: digits, spaces and + only; keep any leading 0.
- courses: the ticked checkboxes only, from exactly CHESS, CODING, ART & DESIGN. An empty list if none are ticked.
- enrolledBefore: "yes", "no", or "" if unanswered.
- Keep Thai text in Thai. Do not translate or transliterate anything.`

// geminiSchema forces structured output, so the reply is parsed rather than
// scraped out of prose.
var geminiSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"name":           fieldSchema(),
		"gender":         fieldSchema(),
		"address":        fieldSchema(),
		"dateOfBirth":    fieldSchema(),
		"email":          fieldSchema(),
		"currentSchool":  fieldSchema(),
		"contactNumber":  fieldSchema(),
		"chessLevel":     fieldSchema(),
		"fideId":         fieldSchema(),
		"fideRating":     fieldSchema(),
		"howDidYouKnow":  fieldSchema(),
		"enrolledBefore": fieldSchema(),
		"previousSchool": fieldSchema(),
		"coursePackage":  fieldSchema(),
		"courses": map[string]any{
			"type":  "array",
			"items": map[string]any{"type": "string"},
		},
	},
}

func fieldSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"value":      map[string]any{"type": "string"},
			"confidence": map[string]any{"type": "number"},
		},
		"required": []string{"value", "confidence"},
	}
}

type gemini struct {
	key   string
	model string
	http  *http.Client
	// base is overridden in tests to point at a local server; production leaves
	// it empty and uses Google's endpoint.
	base string
}

// NewGemini builds the provider. A blank key yields nil, so a deployment with
// no key configured simply has no scanning rather than a broken endpoint.
func NewGemini(key, model string) Provider {
	if strings.TrimSpace(key) == "" {
		return nil
	}
	if model == "" {
		model = defaultGeminiModel
	}
	// Generous next to the other outbound clients: a vision call on a photo is
	// seconds of work, not the sub-second a LINE profile lookup takes.
	return &gemini{key: key, model: model, http: &http.Client{Timeout: 60 * time.Second}}
}

func (g *gemini) Name() string { return "gemini/" + g.model }

func (g *gemini) url() string {
	if g.base != "" {
		return g.base
	}
	return fmt.Sprintf(geminiEndpoint, g.model)
}

// generate runs one vision call and returns the model's JSON reply.
//
// Shared by the form reader and the ID-card reader so the two cannot drift on
// the things that matter to every call: temperature 0, a forced response
// schema, the key in a header rather than the query string, and an upstream
// error body that is never forwarded to the caller.
func (g *gemini) generate(ctx context.Context, prompt string, schema map[string]any, image []byte, mime string) ([]byte, error) {
	body, err := json.Marshal(map[string]any{
		"contents": []any{map[string]any{
			"parts": []any{
				map[string]any{"text": prompt},
				map[string]any{"inline_data": map[string]any{
					"mime_type": mime,
					"data":      base64.StdEncoding.EncodeToString(image),
				}},
			},
		}},
		"generationConfig": map[string]any{
			// Deterministic: reading a document is transcription, not writing.
			"temperature":      0,
			"responseMimeType": "application/json",
			"responseSchema":   schema,
		},
	})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.url(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	// The key goes in a header, not the query string: a URL with a credential
	// in it ends up in proxy logs and error messages.
	req.Header.Set("x-goog-api-key", g.key)

	resp, err := g.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// The upstream body can quote the request, so it is never forwarded to
		// the caller — only the status is, and the caller keeps that internal.
		return nil, fmt.Errorf("gemini: status %d", resp.StatusCode)
	}

	var out struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("gemini: could not decode reply: %w", err)
	}
	if len(out.Candidates) == 0 || len(out.Candidates[0].Content.Parts) == 0 {
		// A safety filter or an unreadable photo both land here.
		return nil, fmt.Errorf("gemini: no candidate returned")
	}
	return []byte(out.Candidates[0].Content.Parts[0].Text), nil
}

func (g *gemini) Extract(ctx context.Context, image []byte, mime string) (*Form, error) {
	raw, err := g.generate(ctx, extractPrompt, geminiSchema, image, mime)
	if err != nil {
		return nil, err
	}
	var form Form
	if err := json.Unmarshal(raw, &form); err != nil {
		return nil, fmt.Errorf("gemini: reply was not the requested shape: %w", err)
	}
	return Sanitise(&form), nil
}

// The ID-card instruction. Explicit about the three mistakes this document
// invites: splitting a Thai name on the wrong space, reading the card's issue
// or expiry date as the date of birth, and answering confidently from a
// passport's machine-readable zone when the glare has eaten half of it.
const idCardPrompt = `You are reading a photograph of a Thai national ID card or a passport.
The text is printed, and may be in Thai, English, or both.

Return only these fields. Rules:
- If you cannot read a value, return an empty string and confidence 0. Never invent a plausible value.
- confidence is 0..1: how sure you are of the characters you read.
- firstName / lastName: as printed. A Thai card prints them on separate labelled lines — use those lines, do not split a single name yourself. If the card shows both Thai and English, return the English.
- dateOfBirth: the holder's date of birth, normalised to YYYY-MM-DD. A card also prints an issue date and an expiry date; those are not it. Thai cards may print a Buddhist-era year (2500+) — convert to the common era by subtracting 543. If the day/month order is ambiguous, still answer but set confidence below 0.5.
- documentType: "thai-id" or "passport", or "" if the image is neither or you are unsure.
- Keep Thai text in Thai. Do not translate or transliterate anything.`

var geminiIDCardSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"firstName":    fieldSchema(),
		"lastName":     fieldSchema(),
		"dateOfBirth":  fieldSchema(),
		"documentType": map[string]any{"type": "string"},
	},
}

func (g *gemini) ExtractIDCard(ctx context.Context, image []byte, mime string) (*IDCard, error) {
	raw, err := g.generate(ctx, idCardPrompt, geminiIDCardSchema, image, mime)
	if err != nil {
		return nil, err
	}
	var card IDCard
	if err := json.Unmarshal(raw, &card); err != nil {
		return nil, fmt.Errorf("gemini: reply was not the requested shape: %w", err)
	}
	return SanitiseIDCard(&card), nil
}
