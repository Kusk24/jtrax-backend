// An event's sections: the categories an arbiter published it as.
//
// chess-results.com has no link for a whole event. Swiss-Manager uploads each
// section (U08 + G08, U10 + G10, …) as its own tournament with its own tnr
// number, and the organiser ties them together by giving every section the
// same "Group ID" — which the site shows as a "Tournament selection" row on
// each section's details page. So one pasted link is enough to find all of
// them, but only from the details view, never the ranking.
//
// Two things make that view awkward to read:
//
//   - For a tournament that ended more than five days ago the site hides every
//     link until a "Show tournament details" button is pressed, to keep search
//     engines from crawling the archive. The button is an ASP.NET form post,
//     so FetchEvent posts it back, the way a browser would.
//   - An organiser who never set a Group ID has no selection row at all. That
//     event is a single section, and FetchEvent says so rather than failing.
package chessresults

import (
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// Section is one published category of an event: its own tnr number and the
// short name the organiser gave it ("U14 + G14").
type Section struct {
	ID   int
	Name string
}

// Event is what the details page says about a whole event.
type Event struct {
	// Name is the event's title without the section in brackets:
	// "WCIB CHESS CHAMPIONSHIP 2025", not "… [U14 + G14]".
	Name string
	// Sections in the organiser's order. Always at least one — the tournament
	// the link pointed at.
	Sections []Section
	// Rounds is the scheduled round count ("Number of rounds"), 0 when the page
	// does not say. The ranking only ever tells how many have been played.
	Rounds int
}

var (
	selectionRow  = regexp.MustCompile(`(?is)<td[^>]*>\s*Tournament selection\s*</td>\s*<td[^>]*>(.*?)</td>`)
	selectionItem = regexp.MustCompile(`(?is)<a[^>]*href="[^"]*tnr(\d+)\.aspx[^"]*"[^>]*>(.*?)</a>|<b>(.*?)</b>`)
	roundsRow     = regexp.MustCompile(`(?is)<td[^>]*>\s*Number of rounds\s*</td>\s*<td[^>]*>\s*(\d+)`)
	hiddenInput   = regexp.MustCompile(`(?is)<input[^>]*type="hidden"[^>]*name="([^"]+)"[^>]*value="([^"]*)"`)
	bracketed     = regexp.MustCompile(`\s*\[([^\]]+)\]\s*$`)
)

// detailsButton is the submit button that reveals an archived event's links.
const detailsButton = "cb_alleDetails"

// FetchEvent reads an event's sections, round count and name from the details
// page of any one of its sections.
func (c *Client) FetchEvent(id int) (*Event, error) {
	u := fmt.Sprintf("%s/tnr%d.aspx?lan=1&turdet=YES", c.base(), id)
	page, final, err := c.getFinal(u)
	if err != nil {
		return nil, err
	}
	if strings.Contains(page, `name="`+detailsButton+`"`) {
		// Posted to where the page came from, not to u — see getFinal.
		if page, err = c.pressDetails(final, page); err != nil {
			return nil, err
		}
	}
	return parseEvent(page, id)
}

// pressDetails posts the page's own form back with the details button, which
// is what a browser does when the button is clicked.
func (c *Client) pressDetails(u, page string) (string, error) {
	form := url.Values{}
	for _, m := range hiddenInput.FindAllStringSubmatch(page, -1) {
		form.Set(m[1], html.UnescapeString(m[2]))
	}
	form.Set(detailsButton, "Show tournament details")
	req, err := http.NewRequest(http.MethodPost, u, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; JTrax school portal)")
	res, err := c.http().Do(req)
	if err != nil {
		return "", fmt.Errorf("chessresults: unreachable: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("chessresults: status %d", res.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, maxPageBytes))
	if err != nil {
		return "", fmt.Errorf("chessresults: read: %w", err)
	}
	return string(raw), nil
}

func parseEvent(page string, id int) (*Event, error) {
	title, _ := headings(page)
	if title == "" {
		return nil, fmt.Errorf("chessresults: page for %d has no tournament heading", id)
	}
	ev := &Event{Name: title}
	// The section's own name, when the title carries it in brackets. It is the
	// fallback name for an event with no selection row.
	own := ""
	if m := bracketed.FindStringSubmatch(title); m != nil {
		own = strings.TrimSpace(m[1])
		ev.Name = strings.TrimSpace(bracketed.ReplaceAllString(title, ""))
	}
	if m := roundsRow.FindStringSubmatch(page); m != nil {
		ev.Rounds, _ = strconv.Atoi(m[1])
	}

	if row := selectionRow.FindStringSubmatch(page); row != nil {
		for _, m := range selectionItem.FindAllStringSubmatch(row[1], -1) {
			switch {
			case m[1] != "":
				n, err := strconv.Atoi(m[1])
				if err != nil || n <= 0 {
					continue
				}
				ev.Sections = append(ev.Sections, Section{ID: n, Name: cleanCell(m[2])})
			case m[3] != "":
				// The section being viewed is printed in bold, not linked.
				ev.Sections = append(ev.Sections, Section{ID: id, Name: cleanCell(m[3])})
			}
		}
	}
	if len(ev.Sections) == 0 {
		name := own
		if name == "" {
			name = ev.Name
		}
		ev.Sections = []Section{{ID: id, Name: name}}
	}
	return ev, nil
}
