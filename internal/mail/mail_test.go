package mail

import "testing"

func TestConfiguredRequiresHostAndFrom(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		want bool
	}{
		{"complete", Config{Host: "smtp.example", From: "jtrax@example"}, true},
		{"no host", Config{From: "jtrax@example"}, false},
		{"no from", Config{Host: "smtp.example"}, false},
		{"empty", Config{}, false},
	}
	for _, c := range cases {
		if got := c.cfg.Configured(); got != c.want {
			t.Errorf("%s: Configured() = %v, want %v", c.name, got, c.want)
		}
	}
	// An unconfigured Config must yield no Sender, so the caller can tell.
	if New(Config{}) != nil {
		t.Error("New on an empty config should return nil")
	}
}

// A newline in the recipient or subject would let a caller append their own
// headers — Bcc, say — and turn this into an open relay. Rejected before any
// network call, so the test needs no SMTP server.
func TestSendRejectsHeaderInjection(t *testing.T) {
	s := &smtpSender{cfg: Config{Host: "127.0.0.1", Port: "1", From: "jtrax@example"}}
	cases := []struct{ name, to, subject string }{
		{"newline in recipient", "victim@example\r\nBcc: everyone@example", "Hello"},
		{"bare newline in recipient", "victim@example\nBcc: everyone@example", "Hello"},
		{"newline in subject", "victim@example", "Hello\r\nBcc: everyone@example"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := s.Send(c.to, c.subject, "body"); err == nil {
				t.Fatal("want an error, got nil")
			}
		})
	}
}
