// Row identifiers shared by the API and school workflows.
package db

import (
	"crypto/rand"
	"encoding/hex"
)

// NewID generates a prefixed identifier with five random bytes.
func NewID(prefix string) string {
	raw := make([]byte, 5)
	rand.Read(raw)
	return prefix + "_" + hex.EncodeToString(raw)
}
