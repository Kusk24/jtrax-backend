// The academy's email layout.
//
// Every email used to be one line of plain text — "Your payment of 13,500 THB
// was successful." — with no sender name in view, no details and nothing to
// click. Each is now an Email: a heading, a greeting, a few paragraphs, an
// optional table of details and an optional button, drawn in one branded
// layout. It goes out as HTML with a plain-text copy beside it, so a mail
// client that shows no HTML still reads properly.
package mail

import (
	"fmt"
	"html"
	"strings"
)

// Detail is one row of the details table: "Amount · 13,500 THB".
type Detail struct {
	Label string
	Value string
}

// Button is the one thing the email asks the reader to do.
type Button struct {
	Label string
	URL   string
}

type Email struct {
	// Heading is the big line at the top: "Payment successful".
	Heading string
	// Greeting opens the text: "Hello Sandy,". Optional.
	Greeting   string
	Paragraphs []string
	Details    []Detail
	Button     *Button
	// Note is small print under the button: expiry, "if this wasn't you".
	Note string
}

const (
	school = "JCA Chess School"
	blue   = "#2E5CB8"
	navy   = "#1E3A70"
	ink    = "#1B2433"
	muted  = "#64708C"
	line   = "#E3E8F2"
	page   = "#F3F6FB"
)

// Text is the plain-text copy.
func (e Email) Text() string {
	var b strings.Builder
	if e.Greeting != "" {
		b.WriteString(e.Greeting + "\n\n")
	}
	for _, p := range e.Paragraphs {
		b.WriteString(p + "\n\n")
	}
	if len(e.Details) > 0 {
		for _, d := range e.Details {
			b.WriteString(d.Label + ": " + d.Value + "\n")
		}
		b.WriteString("\n")
	}
	if e.Button != nil {
		b.WriteString(e.Button.Label + ":\n" + e.Button.URL + "\n\n")
	}
	if e.Note != "" {
		b.WriteString(e.Note + "\n\n")
	}
	b.WriteString(school + "\n")
	return b.String()
}

// HTML is the branded copy: tables and inline styles only, because that is
// what every mail client, Gmail and Outlook included, draws the same way.
func (e Email) HTML() string {
	esc := html.EscapeString
	var body strings.Builder

	if e.Greeting != "" {
		fmt.Fprintf(&body, `<p style="margin:0 0 14px;font-size:15px;color:%s">%s</p>`, ink, esc(e.Greeting))
	}
	for _, p := range e.Paragraphs {
		fmt.Fprintf(&body, `<p style="margin:0 0 14px;font-size:15px;line-height:1.55;color:%s">%s</p>`, ink, esc(p))
	}
	if len(e.Details) > 0 {
		fmt.Fprintf(&body, `<table role="presentation" width="100%%" cellpadding="0" cellspacing="0" style="margin:6px 0 20px;border:1px solid %s;border-radius:10px;border-collapse:separate">`, line)
		for i, d := range e.Details {
			border := ""
			if i > 0 {
				border = "border-top:1px solid " + line + ";"
			}
			fmt.Fprintf(&body,
				`<tr><td style="%spadding:11px 16px;font-size:14px;color:%s">%s</td>`+
					`<td align="right" style="%spadding:11px 16px;font-size:14px;font-weight:600;color:%s">%s</td></tr>`,
				border, muted, esc(d.Label), border, ink, esc(d.Value))
		}
		body.WriteString(`</table>`)
	}
	if e.Button != nil {
		fmt.Fprintf(&body,
			`<table role="presentation" cellpadding="0" cellspacing="0" style="margin:8px 0 18px"><tr>`+
				`<td style="border-radius:9px;background:%s"><a href="%s" style="display:inline-block;padding:12px 22px;font-size:15px;font-weight:600;color:#ffffff;text-decoration:none">%s</a></td>`+
				`</tr></table>`+
				`<p style="margin:0 0 14px;font-size:12.5px;color:%s">If the button doesn't work, copy this link into your browser:<br><a href="%s" style="color:%s;word-break:break-all">%s</a></p>`,
			blue, esc(e.Button.URL), esc(e.Button.Label), muted, esc(e.Button.URL), blue, esc(e.Button.URL))
	}
	if e.Note != "" {
		fmt.Fprintf(&body, `<p style="margin:0;font-size:13px;line-height:1.5;color:%s">%s</p>`, muted, esc(e.Note))
	}

	return fmt.Sprintf(`<!doctype html>
<html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>%[1]s</title></head>
<body style="margin:0;padding:0;background:%[2]s;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,Helvetica,Arial,sans-serif">
<table role="presentation" width="100%%" cellpadding="0" cellspacing="0" style="background:%[2]s;padding:28px 12px">
<tr><td align="center">
<table role="presentation" width="100%%" cellpadding="0" cellspacing="0" style="max-width:560px">
<tr><td style="padding:0 4px 14px;font-size:15px;font-weight:700;color:%[3]s;letter-spacing:.2px">♞ %[4]s</td></tr>
<tr><td style="background:#ffffff;border:1px solid %[5]s;border-radius:14px;padding:28px 26px">
<h1 style="margin:0 0 18px;font-size:21px;line-height:1.3;color:%[3]s">%[1]s</h1>
%[6]s
</td></tr>
<tr><td style="padding:16px 4px 0;font-size:12px;line-height:1.5;color:%[7]s">This is an automatic email from %[4]s. Please don't reply to it — contact the office instead.</td></tr>
</table>
</td></tr>
</table>
</body></html>`, esc(e.Heading), page, navy, school, line, body.String(), muted)
}

// RichSender sends an email in both forms. The SMTP sender is one; a test's
// capturing sender need not be, and still gets the plain text.
type RichSender interface {
	SendRich(to, subject, text, html string) error
}

// Deliver sends e by whatever the sender supports.
func Deliver(s Sender, to, subject string, e Email) error {
	if r, ok := s.(RichSender); ok {
		return r.SendRich(to, subject, e.Text(), e.HTML())
	}
	return s.Send(to, subject, e.Text())
}
