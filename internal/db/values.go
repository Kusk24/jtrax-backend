// Database helpers shared by school workflows.
package db

import (
	"strings"
)

func Nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func NullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// IsUniqueViolation reports whether err is SQLite refusing a duplicate.
//
// Matched on the message because the two drivers this product runs on — modernc
// locally, libSQL remotely — return different error types for it, and neither
// is worth importing here just to type-assert.
func IsUniqueViolation(err error) bool {
	return err != nil && strings.Contains(strings.ToUpper(err.Error()), "UNIQUE CONSTRAINT FAILED")
}
