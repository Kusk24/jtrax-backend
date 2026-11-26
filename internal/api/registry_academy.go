// School announcements and system configuration served by the shared CRUD API.
package api

import (
	"github.com/Kusk24/jtrax-backend/internal/auth"
	"github.com/Kusk24/jtrax-backend/internal/communications"
	"github.com/Kusk24/jtrax-backend/internal/records"
)

func announcementResources() []*records.Resource {
	return []*records.Resource{
		{
			Name: "announcements", Table: "announcement", IDCol: "announcement_id", IDPrefix: "ann",
			Cols: []records.Col{
				{Name: "title", Kind: "text", Required: true},
				{Name: "body", Kind: "text", Required: true},
				{Name: "author_user_account_id", Kind: "text", Required: true},
				{Name: "posted_at", Kind: "text"},
				{Name: "has_attachment", Kind: "bool"},
				// Who it is for (see 0041): every parent, the parents of some
				// classes, or some parents by name — audience_ids holds the
				// classes or parents picked, as a JSON array.
				{Name: "audience", Kind: "text", Enum: []string{"all", "classes", "parents"}},
				{Name: "audience_ids", Kind: "text"},
				// When parents see it, on the academy's clock (see
				// announcementwindow.go). Blank: from posting, no end.
				{Name: "show_from", Kind: "text"},
				{Name: "show_until", Kind: "text"},
			},
			ReadRoles: everyone, WriteRoles: []string{"Teacher"},
			Check: func(row map[string]any) error {
				if err := communications.CheckAnnouncementAudience(row); err != nil {
					return err
				}
				return communications.CheckAnnouncementWindow(row)
			},
			// Resolved once, when it is posted: see resolveAnnouncementAudience.
			AfterWrite: communications.ResolveAnnouncementAudience,
			Scope: map[string]records.ScopeFn{
				// Everything addressed to every parent, and whatever was
				// addressed to this one.
				// Only inside its window.
				"Parent": func(id *auth.Identity) (string, []any) {
					now := communications.AcademyMinute()
					return `(audience = 'all' OR announcement_id IN
						(SELECT announcement_id FROM announcement_recipient WHERE parent_id = ?))
						AND ` + communications.VisibleNow,
						[]any{id.ParentID, now, now}
				},
				// Announcements are for families; students are not an audience.
				"Student": func(id *auth.Identity) (string, []any) {
					return `1 = 0`, nil
				},
			},
		},
	}
}

func configurationResources() []*records.Resource {
	return []*records.Resource{
		{
			Name: "system-configuration", Table: "system_configuration", IDCol: "config_key", IDPrefix: "",
			Cols: []records.Col{
				{Name: "config_value", Kind: "text", Required: true},
			},
			// Readable by every signed-in role: the parent portal shows the
			// certificate milestone (certificate_hours), and the rest is
			// display configuration of the same kind. That is a rule about
			// this table — secrets live in the environment, never here, or
			// this line has to change. Writes stay staff-only.
			ReadRoles: everyone,
		},
	}
}
