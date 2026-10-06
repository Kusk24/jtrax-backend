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

// Detail is one row of the details table: "Amount · 13,500 THB". A row with
// no Value is a section heading inside the table: "Student Login".
type Detail struct {
	Label string
	Value string
}

// Choice is one row of answer buttons under a label.
type Choice struct {
	Label   string
	Buttons []Button
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
	// Second is a quieter, outlined button beside the first: "Open JTrax".
	Second *Button
	// Choices are a question per row — a child's name and the answers to
	// pick from, each a button: "Penny · Attending · Not attending". Drawn
	// after the details, before Button. The first answer is filled, the
	// rest outlined.
	Choices []Choice
	// After is text under the button. A line starting "- " is a bullet;
	// consecutive bullets make one list.
	After []string
	// Note is small print under the button: expiry, "if this wasn't you".
	Note string
	// Signoff closes the letter, one line each: "Best regards,", the school,
	// the portal it is from.
	Signoff []string
}

const (
	school    = "JCA Chess School"
	system    = "JTrax — Chess School Management System"
	automated = "This is an automated email from JCA Chess School. Please do not reply directly to this message."
	blue      = "#2E5CB8"
	navy      = "#1E3A70"
	ink       = "#1B2433"
	muted     = "#64708C"
	line      = "#E3E8F2"
	page      = "#F3F6FB"
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
		for i, d := range e.Details {
			if d.Value == "" {
				if i > 0 {
					b.WriteString("\n")
				}
				b.WriteString(d.Label + "\n")
				continue
			}
			b.WriteString(d.Label + ": " + d.Value + "\n")
		}
		b.WriteString("\n")
	}
	for _, c := range e.Choices {
		b.WriteString(c.Label + "\n")
		for _, btn := range c.Buttons {
			b.WriteString("  " + btn.Label + ": " + btn.URL + "\n")
		}
		b.WriteString("\n")
	}
	if e.Button != nil {
		b.WriteString(e.Button.Label + ":\n" + e.Button.URL + "\n\n")
	}
	if e.Second != nil {
		b.WriteString(e.Second.Label + ":\n" + e.Second.URL + "\n\n")
	}
	for i, p := range e.After {
		item, bullet := strings.CutPrefix(p, "- ")
		nextBullet := i+1 < len(e.After) && strings.HasPrefix(e.After[i+1], "- ")
		switch {
		case bullet && nextBullet:
			b.WriteString("• " + item + "\n")
		case bullet:
			b.WriteString("• " + item + "\n\n")
		case nextBullet:
			b.WriteString(p + "\n")
		default:
			b.WriteString(p + "\n\n")
		}
	}
	if e.Note != "" {
		b.WriteString(e.Note + "\n\n")
	}
	if len(e.Signoff) > 0 {
		b.WriteString("\n" + strings.Join(e.Signoff, "\n") + "\n\n")
	}
	c := CurrentContact()
	b.WriteString("—\n" + school + "\n" + system + "\n" + footerContact(c) + "\n")
	for _, a := range c.Addresses() {
		b.WriteString(a + "\n")
	}
	b.WriteString("\n" + automated + "\n")
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
			if d.Value == "" {
				fmt.Fprintf(&body,
					`<tr><td colspan="2" style="%spadding:10px 16px 8px;background:%s;font-size:12.5px;font-weight:700;letter-spacing:.04em;text-transform:uppercase;color:%s">%s</td></tr>`,
					border, page, navy, esc(d.Label))
				continue
			}
			fmt.Fprintf(&body,
				`<tr><td style="%spadding:11px 16px;font-size:14px;color:%s">%s</td>`+
					`<td align="right" style="%spadding:11px 16px;font-size:14px;font-weight:600;color:%s">%s</td></tr>`,
				border, muted, esc(d.Label), border, ink, esc(d.Value))
		}
		body.WriteString(`</table>`)
	}
	for _, c := range e.Choices {
		fmt.Fprintf(&body,
			`<table role="presentation" width="100%%" cellpadding="0" cellspacing="0" style="margin:0 0 12px;border:1px solid %s;border-radius:10px;border-collapse:separate"><tr><td style="padding:12px 16px">`+
				`<p style="margin:0 0 10px;font-size:15px;font-weight:700;color:%s">%s</p>`+
				`<table role="presentation" cellpadding="0" cellspacing="0"><tr>`,
			line, ink, esc(c.Label))
		for i, btn := range c.Buttons {
			if i > 0 {
				body.WriteString(`<td style="width:8px"></td>`)
			}
			if i == 0 {
				fmt.Fprintf(&body,
					`<td style="border-radius:9px;background:%s"><a href="%s" style="display:inline-block;padding:10px 18px;font-size:14.5px;font-weight:600;color:#ffffff;text-decoration:none">%s</a></td>`,
					blue, esc(btn.URL), esc(btn.Label))
			} else {
				fmt.Fprintf(&body,
					`<td style="border-radius:9px;border:1.5px solid %s"><a href="%s" style="display:inline-block;padding:8.5px 16px;font-size:14.5px;font-weight:600;color:%s;text-decoration:none">%s</a></td>`,
					muted, esc(btn.URL), ink, esc(btn.Label))
			}
		}
		body.WriteString(`</tr></table></td></tr></table>`)
	}
	if e.Button != nil {
		fmt.Fprintf(&body,
			`<table role="presentation" cellpadding="0" cellspacing="0" style="margin:8px 0 18px"><tr>`+
				`<td style="border-radius:9px;background:%s"><a href="%s" style="display:inline-block;padding:12px 22px;font-size:15px;font-weight:600;color:#ffffff;text-decoration:none">%s</a></td>`,
			blue, esc(e.Button.URL), esc(e.Button.Label))
		if e.Second != nil {
			fmt.Fprintf(&body,
				`<td style="width:10px"></td>`+
					`<td style="border-radius:9px;border:1.5px solid %s"><a href="%s" style="display:inline-block;padding:10.5px 20px;font-size:15px;font-weight:600;color:%s;text-decoration:none">%s</a></td>`,
				blue, esc(e.Second.URL), blue, esc(e.Second.Label))
		}
		fmt.Fprintf(&body,
			`</tr></table>`+
				`<p style="margin:0 0 14px;font-size:12.5px;color:%s">If the button doesn't work, copy this link into your browser:<br><a href="%s" style="color:%s;word-break:break-all">%s</a></p>`,
			muted, esc(e.Button.URL), blue, esc(e.Button.URL))
	}
	inList := false
	for _, p := range e.After {
		if item, ok := strings.CutPrefix(p, "- "); ok {
			if !inList {
				fmt.Fprintf(&body, `<ul style="margin:0 0 14px;padding-left:20px;font-size:14.5px;line-height:1.55;color:%s">`, ink)
				inList = true
			}
			fmt.Fprintf(&body, `<li style="margin:0 0 4px">%s</li>`, esc(item))
			continue
		}
		if inList {
			body.WriteString(`</ul>`)
			inList = false
		}
		fmt.Fprintf(&body, `<p style="margin:0 0 14px;font-size:15px;line-height:1.55;color:%s">%s</p>`, ink, esc(p))
	}
	if inList {
		body.WriteString(`</ul>`)
	}
	if e.Note != "" {
		fmt.Fprintf(&body, `<p style="margin:0 0 14px;font-size:13px;line-height:1.5;color:%s">%s</p>`, muted, esc(e.Note))
	}
	if len(e.Signoff) > 0 {
		fmt.Fprintf(&body, `<p style="margin:18px 0 0;font-size:15px;line-height:1.55;color:%s">`, ink)
		for i, l := range e.Signoff {
			if i > 0 {
				body.WriteString("<br>")
			}
			if i == 1 {
				fmt.Fprintf(&body, `<strong>%s</strong>`, esc(l))
			} else {
				body.WriteString(esc(l))
			}
		}
		body.WriteString(`</p>`)
	}

	c := CurrentContact()
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
<tr><td style="padding:16px 4px 0;font-size:12px;line-height:1.6;color:%[7]s"><strong style="color:%[3]s">%[4]s</strong><br>%[8]s<br>%[9]s%[11]s<br><br>%[10]s</td></tr>
</table>
</td></tr>
</table>
</body></html>`, esc(e.Heading), page, navy, school, line, body.String(), muted,
		esc(system), esc(footerContact(c)), esc(automated), addressLines(c))
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

// footerContact is the footer's one line: phone · email · LINE.
func footerContact(c Contact) string {
	line := strings.TrimPrefix(strings.TrimPrefix(c.LINE, "https://"), "http://")
	return c.Phone + " · " + c.Email + " · LINE " + line
}

// addressLines are the footer's address lines, each on its own line.
func addressLines(c Contact) string {
	var b strings.Builder
	for _, a := range c.Addresses() {
		b.WriteString("<br>" + html.EscapeString(a))
	}
	return b.String()
}
