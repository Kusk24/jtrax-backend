// Package academytime is the academy's clock: what day it is in Bangkok,
// whatever zone the server happens to run in.
//
// A "day" in this product — the daily puzzle set, a practice streak, an
// early-bird deadline, when credits expire — is the academy's day. The server
// used to take it from its own local clock, which on a host set to UTC ends the
// academy's day at 07:00: a pupil solving puzzles at 06:30 was recorded on
// yesterday, and the daily set rolled over mid-morning. On a machine set to
// Bangkok the code agreed with itself but not with SQLite's date('now'), which
// is always UTC.
//
// Moments in time — a row's created_at, when a puzzle was opened — are still
// stored in UTC. Only calendar days come from here.
package academytime

import "time"

// DayLayout is how a calendar day is written throughout the schema.
const DayLayout = "2006-01-02"

// zone is Bangkok. Fixed rather than loaded from the tz database: Thailand has
// kept UTC+7 without daylight saving since 1920, and a fixed zone needs no
// tzdata on the host — a minimal image or a stripped server may have none.
var zone = time.FixedZone("ICT", 7*60*60)

var clock = time.Now

// Now is the current moment on the academy's wall clock.
func Now() time.Time { return clock().In(zone) }

// Today is the academy's current calendar day, as the schema writes it.
func Today() string { return Now().Format(DayLayout) }

// Freeze makes Now return t until the returned function is called. It is for
// tests that need a particular hour — the early morning, when the academy's day
// and UTC's disagree — without waiting for one.
func Freeze(t time.Time) (restore func()) {
	prev := clock
	clock = func() time.Time { return t }
	return func() { clock = prev }
}
