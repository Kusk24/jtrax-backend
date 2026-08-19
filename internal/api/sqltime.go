// Converting between SQLite's timestamp format and the wire's.
//
// The schema follows the house convention and lets SQLite stamp rows with
// datetime('now'), which is "2006-01-02 15:04:05" in UTC. That string is not
// ISO 8601, and browsers parse it as *local* time — which would show a Bangkok
// receptionist every message seven hours out. So it is stored in the house
// format and converted on the way out.
//
// Lived in line.go under line-prefixed names until Lichess needed the same
// conversion. Nothing about it was ever specific to LINE.
package api

import "time"

const sqliteTimeLayout = "2006-01-02 15:04:05"

func sqliteNow() string { return time.Now().UTC().Format(sqliteTimeLayout) }

// sqliteISO converts a stored timestamp to RFC 3339 for the wire. An
// unparseable value is passed through rather than dropped: a wrong-looking
// timestamp in the UI is easier to diagnose than a missing one.
func sqliteISO(stored string) string {
	t, err := time.Parse(sqliteTimeLayout, stored)
	if err != nil {
		return stored
	}
	return t.UTC().Format(time.RFC3339)
}
