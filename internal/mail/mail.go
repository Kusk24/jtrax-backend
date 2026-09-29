// Package mail sends transactional email over plain SMTP.
//
// SMTP rather than a provider SDK so the host is a configuration choice, not a
// code dependency: Brevo, Resend, Gmail and a self-hosted relay all speak it,
// and switching means editing environment variables. Every credential comes
// from the environment; nothing here is ever committed.
package mail

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
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

// Config is read once at startup.
//
// Two portal URLs because the deployment has two frontends on different
// domains: staff use the admin console, everyone else uses the web app. Which
// one a reset link points at is decided from the account's role on the server —
// never from anything the caller sends, or a request could aim the link at an
// attacker's host.
type Config struct {
	Host     string
	Port     string
	Username string
	Password string
	From     string
	AppURL   string
	AdminURL string
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
		AdminURL: strings.TrimSuffix(os.Getenv("ADMIN_URL"), "/"),
	}
}

// PortalFor returns the base URL a reset link should use for a role. Staff go
// to the console; everyone else to the web app. Falls back to AppURL so a
// deployment that has not set ADMIN_URL still sends a working link rather than
// one with an empty host.
func (c Config) PortalFor(role string) string {
	if (role == "Admin" || role == "Receptionist") && c.AdminURL != "" {
		return c.AdminURL
	}
	return c.AppURL
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
	if err := checkHeaders(to, subject); err != nil {
		return err
	}
	msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\n"+
		"Content-Type: text/plain; charset=UTF-8\r\n\r\n%s\r\n", s.cfg.From, to, encodeSubject(subject), body)
	return s.deliver(to, msg)
}

// SendRich sends the HTML layout with its plain-text copy as alternatives;
// the client shows whichever it can.
func (s *smtpSender) SendRich(to, subject, text, htmlBody string) error {
	if err := checkHeaders(to, subject); err != nil {
		return err
	}
	boundary := "jtrax-" + randomBoundary()
	msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\n"+
		"Content-Type: multipart/alternative; boundary=%q\r\n\r\n"+
		"--%s\r\nContent-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: base64\r\n\r\n%s\r\n"+
		"--%s\r\nContent-Type: text/html; charset=UTF-8\r\nContent-Transfer-Encoding: base64\r\n\r\n%s\r\n"+
		"--%s--\r\n",
		s.cfg.From, to, encodeSubject(subject), boundary,
		boundary, base64Lines(text),
		boundary, base64Lines(htmlBody),
		boundary)
	return s.deliver(to, msg)
}

// checkHeaders refuses header injection: a newline in either field would let
// a caller append their own headers and turn this into an open relay.
func checkHeaders(to, subject string) error {
	if strings.ContainsAny(to, "\r\n") || strings.ContainsAny(subject, "\r\n") {
		return errors.New("mail: header field contains a newline")
	}
	return nil
}

// encodeSubject keeps a subject with anything beyond ASCII — a Thai course
// name, a dash — readable, as RFC 2047 asks.
func encodeSubject(subject string) string {
	return mime.QEncoding.Encode("UTF-8", subject)
}

// base64Lines encodes a body in the 76-column lines SMTP expects, so a long
// HTML line is never cut by a relay.
func base64Lines(body string) string {
	enc := base64.StdEncoding.EncodeToString([]byte(body))
	var b strings.Builder
	for len(enc) > 76 {
		b.WriteString(enc[:76] + "\r\n")
		enc = enc[76:]
	}
	b.WriteString(enc)
	return b.String()
}

func randomBoundary() string {
	raw := make([]byte, 12)
	_, _ = rand.Read(raw)
	return hex.EncodeToString(raw)
}

func (s *smtpSender) deliver(to, msg string) error {
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
