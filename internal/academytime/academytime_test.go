package academytime

import (
	"testing"
	"time"
)

// The hours that matter are the seven after midnight in Bangkok, when UTC is
// still on the previous day. Those are the hours the old clock got wrong.
func TestTheAcademyDayTurnsAtBangkokMidnight(t *testing.T) {
	cases := []struct {
		utc  string
		want string
	}{
		{"2026-09-20T16:59:59Z", "2026-09-20"}, // 23:59:59 in Bangkok
		{"2026-09-20T17:00:00Z", "2026-09-21"}, // midnight in Bangkok
		{"2026-09-20T23:30:00Z", "2026-09-21"}, // 06:30 — UTC still says the 20th
		{"2026-09-21T00:00:00Z", "2026-09-21"}, // 07:00
	}
	for _, tc := range cases {
		at, _ := time.Parse(time.RFC3339, tc.utc)
		restore := Freeze(at)
		got := Today()
		restore()
		if got != tc.want {
			t.Errorf("at %s UTC the academy's day is %s, want %s", tc.utc, got, tc.want)
		}
	}
}

// Whatever zone the process runs in must not move the day.
func TestTheServersOwnZoneDoesNotMoveTheDay(t *testing.T) {
	at, _ := time.Parse(time.RFC3339, "2026-09-20T23:30:00Z")
	for _, loc := range []*time.Location{time.UTC, time.FixedZone("PST", -8*3600), time.FixedZone("ICT", 7*3600)} {
		restore := Freeze(at.In(loc))
		got := Today()
		restore()
		if got != "2026-09-21" {
			t.Errorf("with the server in %s the academy's day is %s, want 2026-09-21", loc, got)
		}
	}
}
