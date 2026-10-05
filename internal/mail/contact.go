package mail

import (
	"strings"
	"sync"
)

// Contact is how a family reaches the school: what every email's footer
// gives. The values are the office's own (Settings → Academy Contact); the
// server tells this package where to read them with SetContactSource.
type Contact struct {
	Phone   string
	Email   string
	LINE    string // a link, e.g. https://lin.ee/7fhq3N1
	Address string // one address per line
}

// The school's details as its website gives them — used until the office
// saves its own, and wherever a field is left empty.
var defaultContact = Contact{
	Phone:   "02-853-9836 / 099-0156-156",
	Email:   "jcachess@gmail.com",
	LINE:    "https://lin.ee/7fhq3N1",
	Address: "Room As023, As024, 4th Floor, Paradise Park Mall, No. 61 Srinakarin Road, Nong Bon Subdistrict, Prawet District, Bangkok 10250",
}

var (
	sourceMu sync.RWMutex
	source   func() Contact
)

// SetContactSource says where the office's saved details come from.
func SetContactSource(f func() Contact) {
	sourceMu.Lock()
	source = f
	sourceMu.Unlock()
}

// CurrentContact is the office's details, with any empty field filled from
// the website's.
func CurrentContact() Contact {
	sourceMu.RLock()
	f := source
	sourceMu.RUnlock()
	c := Contact{}
	if f != nil {
		c = f()
	}
	if strings.TrimSpace(c.Phone) == "" {
		c.Phone = defaultContact.Phone
	}
	if strings.TrimSpace(c.Email) == "" {
		c.Email = defaultContact.Email
	}
	if strings.TrimSpace(c.LINE) == "" {
		c.LINE = defaultContact.LINE
	}
	if strings.TrimSpace(c.Address) == "" {
		c.Address = defaultContact.Address
	}
	return c
}

// Phones splits "02-853-9836 / 099-0156-156" into its numbers.
func (c Contact) Phones() []string {
	var out []string
	for _, p := range strings.FieldsFunc(c.Phone, func(r rune) bool { return r == '/' || r == ',' || r == '\n' }) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// Addresses is one entry per line of Address.
func (c Contact) Addresses() []string {
	var out []string
	for _, a := range strings.Split(c.Address, "\n") {
		if a = strings.TrimSpace(a); a != "" {
			out = append(out, a)
		}
	}
	return out
}
