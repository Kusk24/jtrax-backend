// Package mail sends transactional email over plain SMTP.
//
// SMTP rather than a provider SDK so the host is a configuration choice, not a
// code dependency: Brevo, Resend, Gmail and a self-hosted relay all speak it,
// and switching means editing environment variables. Every credential comes
// from the environment; nothing here is ever committed.
package mail

import (
	"errors"
	"fmt"
	"net/smtp"
	"os"
	"strings"
)

// Sender delivers one message. The interface exists so tests can capture mail
// without a network, and so a missing configuration is an explicit no-op
// rather than a nil dereference.
type Sender interface {
	Send(to, subject, body string) error
}

// Config is read once at startup. AppURL is the public address of the portal
// the reset link should point at.
type Config struct {
	Host     string
	Port     string
	Username string
	Password string
	From     string
	AppURL   string
}

// FromEnv reads the SMTP settings. Missing settings are not an error: a
// developer running the API locally should not have to stand up a mail server,
// so the caller gets a Sender that logs instead.
func FromEnv() Config {
	return Config{
		Host:     os.Getenv("SMTP_HOST"),
		Port:     firstSet(os.Getenv("SMTP_PORT"), "587"),
		Username: os.Getenv("SMTP_USERNAME"),
		Password: os.Getenv("SMTP_PASSWORD"),
		From:     os.Getenv("MAIL_FROM"),
		AppURL:   strings.TrimSuffix(os.Getenv("APP_URL"), "/"),
	}
}

// Configured reports whether mail can actually be delivered.
func (c Config) Configured() bool {
	return c.Host != "" && c.From != ""
}

type smtpSender struct{ cfg Config }

// New returns an SMTP sender, or nil when the environment is not configured.
func New(cfg Config) Sender {
	if !cfg.Configured() {
		return nil
	}
	return &smtpSender{cfg: cfg}
}

func (s *smtpSender) Send(to, subject, body string) error {
	if strings.ContainsAny(to, "\r\n") || strings.ContainsAny(subject, "\r\n") {
		// Header injection: a newline in either field would let a caller append
		// their own headers and turn this into an open relay.
		return errors.New("mail: header field contains a newline")
	}
	msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\n"+
		"Content-Type: text/plain; charset=UTF-8\r\n\r\n%s\r\n", s.cfg.From, to, subject, body)

	addr := s.cfg.Host + ":" + s.cfg.Port
	var auth smtp.Auth
	if s.cfg.Username != "" {
		auth = smtp.PlainAuth("", s.cfg.Username, s.cfg.Password, s.cfg.Host)
	}
	return smtp.SendMail(addr, auth, s.cfg.From, []string{to}, []byte(msg))
}

func firstSet(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
