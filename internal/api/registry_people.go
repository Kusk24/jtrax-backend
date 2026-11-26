// People, family contacts and notification preferences served by the shared CRUD API.
package api

import (
	"github.com/Kusk24/jtrax-backend/internal/families"
	"github.com/Kusk24/jtrax-backend/internal/records"
)

func peopleResources() []*records.Resource {
	return []*records.Resource{
		{
			Name: "students", Table: "student", IDCol: "student_id", IDPrefix: "stu",
			Check: families.CheckStudentDOB,
			Cols: []records.Col{
				{Name: "user_account_id", Kind: "text"},
				{Name: "name", Kind: "text", Required: true},
				{Name: "date_of_birth", Kind: "text"},
				{Name: "current_level", Kind: "text"},
				// The school the child attends the rest of the week. The desk
				// tells two same-named children apart by it, and a pickup has
				// to fit around its own bell.
				{Name: "current_school", Kind: "text"},
				// Which site the child is enrolled at. One today; the console
				// has always displayed it and never stored it, so every row
				// said "Bangkok" whether or not anyone had chosen it.
				{Name: "branch", Kind: "text"},
				{Name: "fide_rating", Kind: "real"},
				// The join key to external tournament tables: a FIDE ID names
				// this child in any arbiter's standings for life, which their
				// name never reliably does.
				{Name: "fide_id", Kind: "text"},
				{Name: "last_attended_date", Kind: "text"},
				{Name: "streak_count", Kind: "int"},
				// Left the academy: set to archive, cleared to restore. The
				// record and its history stay either way.
				{Name: "archived_at", Kind: "text"},
				{Name: "archived_reason", Kind: "text"},
			},
			AfterWrite: families.StudentAfterWrite,
			// The login email, which the student table deliberately does not
			// duplicate. Staff only: teachers can already list every student,
			// and a name is not an email address.
			Derived: []records.Derived{
				{Name: "email", Expr: "(SELECT ua.email FROM user_account ua WHERE ua.user_account_id = student.user_account_id)"},
			},
			ReadRoles: everyone,
			Scope: map[string]records.ScopeFn{
				"Parent":  byParentStudents("student_id"),
				"Student": byOwnStudent("student_id"),
			},
		},
		{
			Name: "parents", Table: "parent", IDCol: "parent_id", IDPrefix: "par",
			Cols: []records.Col{
				{Name: "user_account_id", Kind: "text", Required: true},
				{Name: "name", Kind: "text", Required: true},
				// Set when their last child here was archived (0079).
				{Name: "archived_at", Kind: "text"},
			},
			Derived: []records.Derived{
				{Name: "email", Expr: "(SELECT ua.email FROM user_account ua WHERE ua.user_account_id = parent.user_account_id)"},
			},
			ReadRoles: []string{"Parent"},
			Scope:     map[string]records.ScopeFn{"Parent": byOwnParent("parent_id")},
		},
		{
			Name: "teachers", Table: "teacher", IDCol: "teacher_id", IDPrefix: "tch",
			Cols: []records.Col{
				// Optional since 0023. The academy has no teacher workflow and
				// issues no teacher logins, so a teacher is a record of who
				// teaches — the Academy list, and the name on a parent's class
				// card. Rows written before that still carry their account.
				{Name: "user_account_id", Kind: "text"},
				{Name: "name", Kind: "text", Required: true},
				{Name: "phone", Kind: "text"},
				{Name: "email", Kind: "text"},
				{Name: "line_id", Kind: "text"},
			},
			ReadRoles: everyone,
		},
		{
			Name: "admins", Table: "admin", IDCol: "admin_id", IDPrefix: "adm",
			Cols: []records.Col{
				{Name: "user_account_id", Kind: "text", Required: true},
				{Name: "name", Kind: "text", Required: true},
				{Name: "phone", Kind: "text"},
				{Name: "email", Kind: "text"},
				{Name: "line_id", Kind: "text"},
			},
		},
		{
			Name: "parent-contacts", Table: "parent_contact", IDCol: "parent_contact_id", IDPrefix: "pct",
			Cols: []records.Col{
				{Name: "parent_id", Kind: "text", Required: true},
				{Name: "contact_type", Kind: "text", Enum: contactTypes, Required: true},
				{Name: "value", Kind: "text", Required: true},
			},
			// A parent reads their own; only the office changes them — the
			// profile in the portal is read-only.
			ReadRoles: []string{"Parent"},
			Scope:     map[string]records.ScopeFn{"Parent": byOwnParent("parent_id")},
			Own:       ownParentRow,
		},
		{
			Name: "student-parents", Table: "student_parent", IDCol: "student_id", IDPrefix: "",
			Cols: []records.Col{
				{Name: "parent_id", Kind: "text", Required: true},
				{Name: "relationship_type", Kind: "text"},
			},
			ReadRoles: []string{"Parent"},
			Scope:     map[string]records.ScopeFn{"Parent": byOwnParent("parent_id")},
		},
		{
			Name: "notification-preferences", Table: "notification_preference", IDCol: "parent_id", IDPrefix: "",
			Cols: []records.Col{
				{Name: "check_in_alerts_enabled", Kind: "bool"},
				{Name: "credit_expiry_alerts_enabled", Kind: "bool"},
				{Name: "announcement_alerts_enabled", Kind: "bool"},
			},
			ReadRoles: []string{"Parent"}, WriteRoles: []string{"Parent"},
			Scope: map[string]records.ScopeFn{"Parent": byOwnParent("parent_id")},
			Own:   ownParentRow,
		},
	}
}
