package api

import "testing"

// What counts as the same place, on both sides of the allowlist.
//
// The old code trimmed one trailing slash off the configured value and
// compared strings, so `APP_URL=https://portal/app` matched nothing and a
// phone's `jtraxmobileapp://` became `jtraxmobileapp:/`. Both sides go through
// this now.
func TestOriginOf(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"https://portal.test", "https://portal.test"},
		{"https://portal.test/", "https://portal.test"},
		{"  https://portal.test  ", "https://portal.test"},
		// A configured value with a path: the origin is what matters.
		{"https://portal.test/app/", "https://portal.test"},
		// A phone. No host at all, which is the case that used to break.
		{"jtraxmobileapp://", "jtraxmobileapp://"},
		{"jtraxmobileapp:///student/profile", "jtraxmobileapp://"},
		{"jtraxmobileapp://student/profile", "jtraxmobileapp://student"},
		// Nothing configured, and nothing that could be an origin.
		{"", ""},
		{"   ", ""},
		{"portal.test", ""},
		{"/student/profile", ""},
	} {
		if got := originOf(tc.in); got != tc.want {
			t.Errorf("originOf(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
