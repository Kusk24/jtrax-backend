// Why a scan failed, said to whoever is holding the phone. Every provider
// failure used to answer "could not read the card", so a tester could not tell
// a refused key from a blurry photo without the server log.
//
// A failed scan now carries a reason code, and the provider's HTTP status when
// it answered with one. Only those two go out, never the provider's reply,
// which can quote the child's document.
package api

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"

	"github.com/Kusk24/jtrax-backend/internal/httpx"
	"github.com/Kusk24/jtrax-backend/internal/ocr"
)

// providerFailure names what went wrong with a call to the vision provider:
// a reason code and the status it answered with, 0 when it never answered.
// Settings' Test button and the scans share it, so the two say the same thing
// for the same failure.
func providerFailure(err error) (reason string, status int) {
	var answered *ocr.StatusError
	switch {
	case errors.As(err, &answered):
		switch answered.Status {
		case http.StatusNotFound:
			return "model_unavailable", answered.Status
		case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden:
			return "key_rejected", answered.Status
		case http.StatusTooManyRequests:
			return "quota", answered.Status
		default:
			return "provider_error", answered.Status
		}
	case errors.Is(err, ocr.ErrNoCandidate):
		// Answered, with nothing: a safety filter or an unreadable photo.
		return "no_answer", 0
	case errors.Is(err, context.DeadlineExceeded):
		return "unreachable", 0
	}
	// A *url.Error is the request never getting an answer: DNS, TLS, a refused
	// connection, the client's own timeout.
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return "unreachable", 0
	}
	return "provider_error", 0
}

// providerFailureSentence is the reason in words, after "could not read the
// card: ". English, for the admin console and for any client that does not
// translate the code itself.
func providerFailureSentence(reason string, status int) string {
	switch reason {
	case "model_unavailable":
		return fmt.Sprintf("the scan model is not available (status %d) — an admin can choose another under Settings → Form scanning", status)
	case "key_rejected":
		return fmt.Sprintf("the scan service refused the server's key (status %d)", status)
	case "quota":
		return fmt.Sprintf("the scan service's limit is used up (status %d) — try again later", status)
	case "no_answer":
		return "the scan service gave no answer for this photo — try a clearer, straighter photo"
	case "unreachable":
		return "the scan service could not be reached — try again"
	}
	if status != 0 {
		return fmt.Sprintf("the scan service answered with an error (status %d)", status)
	}
	return "the scan service's answer could not be used"
}

// scanFailed writes a failed scan's reply: the sentence, the reason code, and
// the provider's status when there is one. The full error goes to the log only.
func scanFailed(w http.ResponseWriter, code int, msg, reason string, status int, internal error) {
	if internal != nil {
		// The same line httpx.Error logs.
		log.Printf("http %d: %s: %v", code, msg, internal)
	}
	out := map[string]any{"error": msg, "reason": reason}
	if status != 0 {
		out["status"] = status
	}
	httpx.JSON(w, code, out)
}

// providerFailed is scanFailed for an error from the provider itself. what is
// the thing that was being read: "card" or "form".
func providerFailed(w http.ResponseWriter, what string, err error) {
	reason, status := providerFailure(err)
	scanFailed(w, http.StatusBadGateway,
		"could not read the "+what+": "+providerFailureSentence(reason, status), reason, status, err)
}
