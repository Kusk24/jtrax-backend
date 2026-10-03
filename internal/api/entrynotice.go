// What a family is told about an entry they have not paid for yet — after
// leaving Stripe without paying, and in the confirmation email. The rules are
// the ones entryrules.go enforces; this is only their wording, English then
// Thai, with the school's contacts for anything the words do not answer.
package api

import (
	"database/sql"
	"html"
	"net/http"
	"strings"
	"time"
)

// The academy's contacts, as its website (jcachess.ac.th) gives them.
const (
	academyPhone = "02-853-9836 / 099-0156-156"
	academyEmail = "jcachess@gmail.com"
	academyLINE  = "https://lin.ee/7fhq3N1"
)

// longDate writes 2026-09-28 as "28 September 2026" — the email and the page
// are read by families, not parsed.
func longDate(iso string) string {
	d, err := time.Parse("2006-01-02", iso)
	if err != nil {
		return iso
	}
	return d.Format("2 January 2006")
}

// unpaidRules is the rule text for one entry: the early-bird date only when
// the entry was given the early-bird price, the closing date when there is one.
func unpaidRules(earlyBirdUntil, closes string) (en, th []string) {
	en = append(en, "You can pay online with the link in your confirmation email, or at the JCA front desk.")
	th = append(th, "ชำระเงินออนไลน์ได้จากลิงก์ในอีเมลยืนยัน หรือชำระที่เคาน์เตอร์ของ JCA")
	if earlyBirdUntil != "" {
		d := longDate(earlyBirdUntil)
		en = append(en, "You were given the early-bird price. Pay by "+d+" to keep it — after that the regular price applies.")
		th = append(th, "คุณได้รับราคา Early Bird กรุณาชำระภายในวันที่ "+d+" หลังจากนั้นจะคิดราคาปกติ")
	}
	if closes != "" {
		d := longDate(closes)
		en = append(en, "If the fee is not paid by "+d+", when registration closes, the place is released.")
		th = append(th, "หากยังไม่ชำระภายในวันที่ "+d+" ซึ่งเป็นวันปิดรับสมัคร สิทธิ์การแข่งขันจะถูกยกเลิก")
	}
	return en, th
}

func contactLines() (en, th string) {
	en = "Questions? Contact JCA Chess School — phone " + academyPhone + ", email " + academyEmail + ", LINE " + academyLINE
	th = "สอบถามเพิ่มเติม ติดต่อ JCA Chess School โทร " + academyPhone + " อีเมล " + academyEmail + " LINE " + academyLINE
	return en, th
}

// handlePayCancelled is where Stripe sends a family who left without paying.
// For a tournament entry it says what happens next; for anything else, that
// nothing was charged. It shows no personal data — only the event's dates.
func handlePayCancelled(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var b strings.Builder
		b.WriteString(`<h1 style="font-size:21px;margin:0 0 6px">Payment cancelled — nothing was charged.</h1>`)
		b.WriteString(`<p style="margin:0 0 18px;color:#4b5f83">ยกเลิกการชำระเงิน ยังไม่มีการตัดเงิน</p>`)

		if entry := r.URL.Query().Get("entry"); entry != "" {
			var earlyBird sql.NullInt64
			var ebUntil, closes, status string
			err := d.QueryRow(`
				SELECT r.early_bird_applied, COALESCE(t.early_bird_deadline,''),
				       COALESCE(t.registration_deadline,''), r.status
				  FROM tournament_registration r
				  JOIN tournament t ON t.tournament_id = r.tournament_id
				 WHERE r.tournament_registration_id = ?`, entry).
				Scan(&earlyBird, &ebUntil, &closes, &status)
			if err == nil && status == "Approved" {
				if earlyBird.Int64 != 1 {
					ebUntil = ""
				}
				en, th := unpaidRules(ebUntil, closes)
				b.WriteString(`<p style="margin:0 0 8px;font-weight:600">Your place is saved, but it is not paid yet.</p>`)
				b.WriteString(`<ul style="text-align:left;margin:0 0 10px;padding-left:20px;line-height:1.5">`)
				for _, line := range en {
					b.WriteString("<li>" + html.EscapeString(line) + "</li>")
				}
				b.WriteString(`</ul><p style="margin:14px 0 8px;font-weight:600">ที่นั่งของคุณถูกบันทึกไว้แล้ว แต่ยังไม่ได้ชำระเงิน</p>`)
				b.WriteString(`<ul style="text-align:left;margin:0 0 10px;padding-left:20px;line-height:1.5">`)
				for _, line := range th {
					b.WriteString("<li>" + html.EscapeString(line) + "</li>")
				}
				b.WriteString("</ul>")
			}
		}

		b.WriteString(`<div style="margin-top:18px;padding:14px 16px;border-radius:14px;background:#eef4ff;text-align:left;line-height:1.6">`)
		b.WriteString(`<strong>JCA Chess School</strong><br>`)
		b.WriteString(`Phone / โทร: <a href="tel:028539836">02-853-9836</a> · <a href="tel:0990156156">099-0156-156</a><br>`)
		b.WriteString(`Email / อีเมล: <a href="mailto:` + academyEmail + `">` + academyEmail + `</a><br>`)
		b.WriteString(`LINE: <a href="` + academyLINE + `">` + academyLINE + `</a></div>`)

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!doctype html><meta name=viewport content="width=device-width,initial-scale=1">` +
			`<title>JCA Chess Academy</title>` +
			`<body style="font-family:sans-serif;color:#10264d;display:flex;min-height:90vh;align-items:center;justify-content:center;padding:24px">` +
			`<main style="max-width:34em;text-align:center">` + b.String() + `</main>`))
	}
}
