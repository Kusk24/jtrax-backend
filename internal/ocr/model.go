package ocr

// Picking the vision model from the console rather than only from the
// environment. Google renames and withdraws models often enough that finding
// out from a failed scan, then waiting on a redeploy, is the wrong way round.
// The key still comes from the environment only. The model name is not a
// secret, so it can live in system_configuration.

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

// The name is written into the provider's URL path, so anything that could
// change that path (a slash, "..", a query string) is refused. Every current
// Gemini model name fits this.
var modelName = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{0,63}$`)

// ValidModel reports whether name may be sent to a provider as a model name.
func ValidModel(name string) bool { return modelName.MatchString(name) }

// ModelChooser is a provider whose model can be changed while the server runs,
// and which can check that its key and model work before anyone relies on
// them. Optional: a provider without it keeps whatever model it was built with.
type ModelChooser interface {
	Provider
	// Model is the model this provider calls.
	Model() string
	// WithModel returns a copy of the provider that calls model instead. An
	// invalid name leaves the provider as it was.
	WithModel(model string) Provider
	// Test makes the cheapest real call the provider allows. A nil error means
	// a scan would reach the model.
	Test(ctx context.Context) error
}

// ModelInfo is one model the provider's key can call.
type ModelInfo struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
}

// ModelLister is a provider that can say which models its key can call, so
// Settings offers Google's current list rather than one written into the
// admin app. Optional, like ModelChooser.
type ModelLister interface {
	ListModels(ctx context.Context) ([]ModelInfo, error)
}

// Families that cannot read a form: speech, image and video making,
// embeddings, live audio, robotics. Google's list does not say which models
// take a photo, so the rest are told apart by name. The "-latest" aliases are
// left out too: they move to a different model without notice, and share that
// model's daily quota.
var notForScanning = []string{
	"tts", "image", "embedding", "live", "audio", "transcribe", "translate", "robotics",
	"computer-use", "customtools", "nano-banana", "veo", "lyria", "imagen", "latest",
}

// ForScanning reports whether a model, by its name, can read a photo and
// answer in text.
func ForScanning(name string) bool {
	if !strings.HasPrefix(name, "gemini-") || !ValidModel(name) {
		return false
	}
	for _, word := range notForScanning {
		if strings.Contains(name, word) {
			return false
		}
	}
	return true
}

// StatusError is the provider answering with a status other than success. It
// carries only the status: the upstream body can quote the request.
type StatusError struct{ Status int }

func (e *StatusError) Error() string { return fmt.Sprintf("status %d", e.Status) }
