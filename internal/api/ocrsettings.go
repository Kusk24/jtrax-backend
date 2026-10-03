// Which vision model reads the scanned forms, chosen in the admin console, and
// a button there that checks the choice works before anyone scans a real form.
//
// The model name is stored in system_configuration under ocr_model. The API key
// is not: it stays in the environment, and no endpoint here returns it.
//
// Admin only, like the LINE credentials. A receptionist scans forms but does
// not decide which outside service reads them.
package api

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Kusk24/jtrax-backend/internal/httpx"
	"github.com/Kusk24/jtrax-backend/internal/ocr"
)

const ocrModelKey = "ocr_model"

// scannerFor is the provider as the console has configured it: the saved model
// if there is one, otherwise the model the server started with. Read on every
// scan, so a change in Settings applies to the next scan without a restart.
func scannerFor(d *sql.DB, p ocr.Provider) ocr.Provider {
	chooser, ok := p.(ocr.ModelChooser)
	if !ok {
		return p
	}
	// WithModel ignores a name that fails ValidModel, so a bad row written
	// through the generic config endpoint cannot reach the provider's URL.
	if saved := savedOCRModel(d); saved != "" {
		return chooser.WithModel(saved)
	}
	return p
}

func savedOCRModel(d *sql.DB) string {
	var model string
	d.QueryRow(`SELECT config_value FROM system_configuration WHERE config_key = ?`,
		ocrModelKey).Scan(&model)
	return model
}

func mountOCRSettings(mux *http.ServeMux, d *sql.DB, provider ocr.Provider) {
	mux.HandleFunc("GET /api/v1/ocr", handleOCRGet(d, provider))
	mux.HandleFunc("PUT /api/v1/ocr", handleOCRPut(d, provider))
	// Each test is a real call to the provider and counts against the key's
	// quota, so a stuck client pressing it in a loop is limited.
	mux.HandleFunc("POST /api/v1/ocr/test",
		httpx.RateLimit(10, handleOCRTest(d, provider)))
}

// ocrState is what the console shows. It never includes the key.
func ocrState(d *sql.DB, provider ocr.Provider) map[string]any {
	out := map[string]any{
		// False when the server has no key, so scanning is off whatever model
		// is chosen.
		"configured":   provider != nil,
		"savedModel":   savedOCRModel(d),
		"model":        "",
		"defaultModel": "",
	}
	if chooser, ok := provider.(ocr.ModelChooser); ok {
		out["defaultModel"] = chooser.Model()
		if current, ok := scannerFor(d, provider).(ocr.ModelChooser); ok {
			out["model"] = current.Model()
		}
	}
	return out
}

func requireAdmin(d *sql.DB, w http.ResponseWriter, r *http.Request) bool {
	id := requireIdentity(d, w, r)
	if id == nil {
		return false
	}
	if id.Role != "Admin" {
		httpx.Error(w, http.StatusForbidden, "not allowed", nil)
		return false
	}
	return true
}

func handleOCRGet(d *sql.DB, provider ocr.Provider) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireAdmin(d, w, r) {
			return
		}
		httpx.JSON(w, http.StatusOK, ocrState(d, provider))
	}
}

// handleOCRPut saves the model. An empty model removes the saved one, so the
// server goes back to the model it started with.
func handleOCRPut(d *sql.DB, provider ocr.Provider) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireAdmin(d, w, r) {
			return
		}
		var body struct {
			Model string `json:"model"`
		}
		if err := httpx.Decode(r, &body); err != nil {
			httpx.Error(w, http.StatusBadRequest, "invalid body", err)
			return
		}
		model := strings.TrimSpace(body.Model)
		var err error
		if model == "" {
			_, err = d.Exec(`DELETE FROM system_configuration WHERE config_key = ?`, ocrModelKey)
		} else if !ocr.ValidModel(model) {
			httpx.Error(w, http.StatusBadRequest,
				"model name may use only lowercase letters, digits, dots and dashes", nil)
			return
		} else {
			_, err = d.Exec(`INSERT INTO system_configuration (config_key, config_value) VALUES (?, ?)
			                 ON CONFLICT (config_key) DO UPDATE SET config_value = excluded.config_value`,
				ocrModelKey, model)
		}
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not save the model", err)
			return
		}
		httpx.JSON(w, http.StatusOK, ocrState(d, provider))
	}
}

// handleOCRTest checks a model against the server's key. With a model in the
// body it tests that one, so the admin can try a model before saving it. With
// none it tests what scans use now.
//
// A failed test is still a successful request, so it answers 200 with ok false
// and a reason code the console translates. It gives the upstream status and
// nothing else from the provider's reply.
func handleOCRTest(d *sql.DB, provider ocr.Provider) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireAdmin(d, w, r) {
			return
		}
		var body struct {
			Model string `json:"model"`
		}
		if r.ContentLength != 0 {
			if err := httpx.Decode(r, &body); err != nil {
				httpx.Error(w, http.StatusBadRequest, "invalid body", err)
				return
			}
		}
		model := strings.TrimSpace(body.Model)
		if model != "" && !ocr.ValidModel(model) {
			httpx.Error(w, http.StatusBadRequest,
				"model name may use only lowercase letters, digits, dots and dashes", nil)
			return
		}

		result := map[string]any{"ok": false, "model": model}
		if provider == nil {
			result["reason"] = "not_configured"
			httpx.JSON(w, http.StatusOK, result)
			return
		}
		chooser, ok := scannerFor(d, provider).(ocr.ModelChooser)
		if !ok {
			result["reason"] = "not_testable"
			httpx.JSON(w, http.StatusOK, result)
			return
		}
		if model != "" {
			chooser = chooser.WithModel(model).(ocr.ModelChooser)
		}
		result["model"] = chooser.Model()

		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		started := time.Now()
		err := chooser.Test(ctx)
		if err == nil {
			result["ok"] = true
			result["millis"] = time.Since(started).Milliseconds()
			httpx.JSON(w, http.StatusOK, result)
			return
		}
		// Logged in full for whoever reads the server log; the console gets
		// only the reason code and the status.
		log.Printf("ocr test: %s: %v", chooser.Model(), err)
		var status *ocr.StatusError
		switch {
		case errors.As(err, &status):
			result["status"] = status.Status
			switch status.Status {
			case http.StatusNotFound:
				result["reason"] = "model_unavailable"
			case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden:
				result["reason"] = "key_rejected"
			case http.StatusTooManyRequests:
				result["reason"] = "quota"
			default:
				result["reason"] = "provider_error"
			}
		case errors.Is(err, context.DeadlineExceeded):
			result["reason"] = "unreachable"
		default:
			// A *url.Error is the request never getting an answer: DNS, TLS,
			// a refused connection.
			var urlErr *url.Error
			if errors.As(err, &urlErr) {
				result["reason"] = "unreachable"
			} else {
				result["reason"] = "provider_error"
			}
		}
		httpx.JSON(w, http.StatusOK, result)
	}
}
