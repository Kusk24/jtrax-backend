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

// StatusError is the provider answering with a status other than success. It
// carries only the status: the upstream body can quote the request.
type StatusError struct{ Status int }

func (e *StatusError) Error() string { return fmt.Sprintf("status %d", e.Status) }
