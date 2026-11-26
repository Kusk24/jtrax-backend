// Email content and entry facts for tournament families.
package entrymail

import (
	"time"

	"github.com/Kusk24/jtrax-backend/internal/mail"
)

// LongDate writes 2026-09-28 as "28 September 2026" — the email and the page
// are read by families, not parsed.
func LongDate(iso string) string {
	d, err := time.Parse("2006-01-02", iso)
	if err != nil {
		return iso
	}
	return d.Format("2 January 2006")
}

func contactLines() (en, th string) {
	c := mail.CurrentContact()
	en = "Questions? Contact JCA Chess School — phone " + c.Phone + ", email " + c.Email + ", LINE " + c.LINE
	th = "สอบถามเพิ่มเติม ติดต่อ JCA Chess School โทร " + c.Phone + " อีเมล " + c.Email + " LINE " + c.LINE
	return en, th
}
