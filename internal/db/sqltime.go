// Database helpers shared by school workflows.
package db

import (
	"time"
)

const SQLiteTimeLayout = "2006-01-02 15:04:05"

func SQLiteNow() string { return time.Now().UTC().Format(SQLiteTimeLayout) }

// SQLiteISO converts a stored timestamp to RFC 3339 for the wire. An
// unparseable value is passed through rather than dropped: a wrong-looking
// timestamp in the UI is easier to diagnose than a missing one.
func SQLiteISO(stored string) string {
	t, err := time.Parse(SQLiteTimeLayout, stored)
	if err != nil {
		return stored
	}
	return t.UTC().Format(time.RFC3339)
}
