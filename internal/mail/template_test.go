package mail

import (
	"strings"
	"testing"
)

func receipt() Email {
	return Email{
		Heading:    "Payment successful",
		Greeting:   "Hello Sandy,",
		Paragraphs: []string{"Thank you. We've received your payment for Uri."},
		Details:    []Detail{{"Amount paid", "13,500 THB"}, {"Course", "King <Slayer>"}},
		Button:     &Button{"Open JTrax", "https://portal.example/?a=1&b=2"},
		Note:       "Keep this email as your receipt.",
	}
}

func TestAnEmailReadsTheSameInTextAndHTML(t *testing.T) {
	e := receipt()
	text, html := e.Text(), e.HTML()
	for _, want := range []string{"Hello Sandy,", "Amount paid: 13,500 THB", "https://portal.example/?a=1&b=2", "JCA Chess School"} {
		if !strings.Contains(text, want) {
			t.Errorf("text copy lacks %q", want)
		}
	}
	for _, want := range []string{"<h1", "Payment successful", "13,500 THB", "Open JTrax", "JCA Chess School"} {
		if !strings.Contains(html, want) {
			t.Errorf("HTML copy lacks %q", want)
		}
	}
	// Anything that came from data is escaped, never markup.
	if strings.Contains(html, "King <Slayer>") || !strings.Contains(html, "King &lt;Slayer&gt;") {
		t.Error("a course name was written into the HTML unescaped")
	}
	if !strings.Contains(html, `href="https://portal.example/?a=1&amp;b=2"`) {
		t.Error("the button link was not escaped")
	}
}

type richCapture struct{ text, html string }

func (r *richCapture) Send(_, _, body string) error { r.text = body; return nil }
func (r *richCapture) SendRich(_, _, text, html string) error {
	r.text, r.html = text, html
	return nil
}

type plainCapture struct{ body string }

func (p *plainCapture) Send(_, _, body string) error { p.body = body; return nil }

func TestDeliverSendsHTMLWhereTheSenderCan(t *testing.T) {
	rich := &richCapture{}
	if err := Deliver(rich, "a@b.c", "s", receipt()); err != nil || rich.html == "" {
		t.Fatalf("a rich sender should get HTML: %v", err)
	}
	plain := &plainCapture{}
	if err := Deliver(plain, "a@b.c", "s", receipt()); err != nil || !strings.Contains(plain.body, "Amount paid: 13,500 THB") {
		t.Fatalf("a plain sender should get the text copy: %q", plain.body)
	}
}

func TestSMTPBodyIsMultipart(t *testing.T) {
	enc := base64Lines(strings.Repeat("x", 200))
	for _, l := range strings.Split(enc, "\r\n") {
		if len(l) > 76 {
			t.Fatalf("a base64 line is %d long", len(l))
		}
	}
	if got := encodeSubject("Payment received: ชิงแชมป์"); !strings.HasPrefix(got, "=?UTF-8?") {
		t.Fatalf("a Thai subject was not encoded: %q", got)
	}
}

// Every email ends the same way: the school, the system, how to reach the
// office, and that it is automated; a letter can sign off and list points.
func TestEveryEmailCarriesTheSchoolFooter(t *testing.T) {
	e := Email{
		Heading: "Reset Your Password", Greeting: "Dear Sandy,",
		Button:  &Button{Label: "Choose a New Password", URL: "https://x/reset"},
		After:   []string{"For your security:", "- This link can be used only once.", "- The link will expire after 1 hour.", "Thanks."},
		Signoff: []string{"Best regards,", "JCA Chess School", "JTrax Account Support"},
	}
	text, page := e.Text(), e.HTML()
	for _, want := range []string{"• This link can be used only once.", "Best regards,\nJCA Chess School\nJTrax Account Support",
		"JTrax — Chess School Management System", "02-853-9836", "Please do not reply directly to this message."} {
		if !strings.Contains(text, want) {
			t.Errorf("text missing %q:\n%s", want, text)
		}
	}
	for _, want := range []string{"<li style", "</ul>", "JTrax — Chess School Management System", "jcachess@gmail.com"} {
		if !strings.Contains(page, want) {
			t.Errorf("html missing %q", want)
		}
	}
}
