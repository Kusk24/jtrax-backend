// Package httpx holds the API's JSON format and shared Gin middleware.
package httpx

import (
	"encoding/json"
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
)

// JSON preserves the content type and trailing newline used by school clients.
func JSON(ctx *gin.Context, status int, v any) {
	ctx.Header("Content-Type", "application/json")
	ctx.Status(status)
	json.NewEncoder(ctx.Writer).Encode(v)
}

// Error writes a client-safe error envelope. Internal details stay in the log.
func Error(ctx *gin.Context, status int, publicMsg string, internal error) {
	if internal != nil {
		log.Printf("http %d: %s: %v", status, publicMsg, internal)
	}
	JSON(ctx, status, map[string]string{"error": publicMsg})
}

// Decode parses a JSON body into dst, rejecting unknown fields.
func Decode(ctx *gin.Context, dst any) error {
	dec := json.NewDecoder(ctx.Request.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
}

// StreamFlusher checks the underlying HTTP writer before starting an SSE
// response. Gin exposes Flush even when a wrapped transport cannot stream.
func StreamFlusher(ctx *gin.Context) (http.Flusher, bool) {
	if wrapped, ok := ctx.Writer.(interface{ Unwrap() http.ResponseWriter }); ok {
		if _, ok := wrapped.Unwrap().(http.Flusher); !ok {
			return nil, false
		}
	}
	return ctx.Writer, true
}
