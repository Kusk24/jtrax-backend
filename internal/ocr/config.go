package ocr

import "os"

// Config is read from the environment only. The key is a credential: it is
// never committed, never written into a doc, and never returned by an endpoint.
type Config struct {
	// Provider selects the implementation: "gemini", or "" / "none" for off.
	Provider string
	APIKey   string
	Model    string
	// BaseURL overrides the provider endpoint. For pointing a development
	// machine at a local stub so the flow can be exercised without a key and
	// without spending quota; production leaves it unset.
	BaseURL string
}

func FromEnv() Config {
	return Config{
		Provider: os.Getenv("OCR_PROVIDER"),
		APIKey:   os.Getenv("OCR_API_KEY"),
		Model:    os.Getenv("OCR_MODEL"),
		BaseURL:  os.Getenv("OCR_BASE_URL"),
	}
}

// New builds the configured provider, or nil when scanning is switched off.
// Nil is a supported state, not an error: the academy can run the whole product
// without form scanning, and the endpoint answers 503 rather than 500.
func New(c Config) Provider {
	switch c.Provider {
	case "gemini":
		p := NewGemini(c.APIKey, c.Model)
		if g, ok := p.(*gemini); ok && c.BaseURL != "" {
			g.base = c.BaseURL
		}
		return p
	default:
		return nil
	}
}
