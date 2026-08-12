// Package httpx holds JSON helpers and middleware shared by every handler.
package httpx

import (
	"encoding/json"
	"log"
	"net/http"
)

// JSON writes v with the given status.
func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// Error writes a client-safe error envelope. Internal details are logged,
// never echoed to the client.
func Error(w http.ResponseWriter, status int, publicMsg string, internal error) {
	if internal != nil {
		log.Printf("http %d: %s: %v", status, publicMsg, internal)
	}
	JSON(w, status, map[string]string{"error": publicMsg})
}

// Decode parses a JSON body into dst, rejecting unknown fields.
func Decode(r *http.Request, dst any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
}
