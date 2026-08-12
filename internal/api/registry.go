// The resource registry: one Resource definition per ER entity, with the
// role permissions and row scopes that implement the authorization rules.
package api

import (
	"database/sql"

	"github.com/Kusk24/jtrax-backend/internal/auth"
)

// Scope fragments reused across resources. Each binds one arg: the caller's
// parent_id or student_id.
func byParentStudents(col string) ScopeFn {
	return func(id *auth.Identity) (string, []any) {
		return col + " IN (SELECT student_id FROM student_parent WHERE parent_id = ?)", []any{id.ParentID}
	}
}

func byOwnStudent(col string) ScopeFn {
	return func(id *auth.Identity) (string, []any) { return col + " = ?", []any{id.StudentID} }
}

func byOwnParent(col string) ScopeFn {
	return func(id *auth.Identity) (string, []any) { return col + " = ?", []any{id.ParentID} }
}

// ownChild allows a Parent to write rows whose student_id is one of their children.
func ownChild(d *sql.DB, id *auth.Identity, row map[string]any) bool {
	if id.Role == "Student" {
		sid, _ := row["student_id"].(string)
		return sid == id.StudentID
	}
	if id.Role != "Parent" {
		return false
	}
	sid, _ := row["student_id"].(string)
	var n int
	d.QueryRow(`SELECT COUNT(*) FROM student_parent WHERE student_id = ? AND parent_id = ?`, sid, id.ParentID).Scan(&n)
	return n > 0
}

// ownParentRow allows a Parent to write rows keyed by their own parent_id.
func ownParentRow(_ *sql.DB, id *auth.Identity, row map[string]any) bool {
	pid, _ := row["parent_id"].(string)
	return id.Role == "Parent" && pid == id.ParentID
}

var (
	everyone       = []string{"Teacher", "Parent", "Student"}
	sessionStatus  = []string{"Scheduled", "Ongoing", "Completed"}
	enrollStatus   = []string{"Active", "Completed", "Withdrawn"}
	classTypes     = []string{"Private", "Group", "Master"}
	payMethods     = []string{"CreditCard", "BankTransfer", "Cash", "PromptPay"}
	payStatus      = []string{"Paid"}
	creditTxTypes  = []string{"purchase", "consumption", "manual_adjustment"}
	tournamentStat = []string{"Upcoming", "Ongoing", "Completed"}
	contactTypes   = []string{"phone", "email", "line_id"}
)

// Registry lists every CRUD resource served under /api/v1.
func Registry() []*Resource {
	return []*Resource{
		{
			Name: "students", Table: "student", IDCol: "student_id", IDPrefix: "stu",
			Cols: []Col{
				{Name: "user_account_id", Kind: "text"},
				{Name: "name", Kind: "text", Required: true},
				{Name: "date_of_birth", Kind: "text"},
				{Name: "current_level", Kind: "text"},
				{Name: "fide_rating", Kind: "real"},
				{Name: "last_attended_date", Kind: "text"},
				{Name: "streak_count", Kind: "int"},
			},
			// The login email, which the student table deliberately does not
			// duplicate. Staff only: teachers can already list every student,
			// and a name is not an email address.
			Derived: []Derived{
				{Name: "email", Expr: "(SELECT ua.email FROM user_account ua WHERE ua.user_account_id = student.user_account_id)"},
			},
			ReadRoles: everyone,
			Scope: map[string]ScopeFn{
				"Parent":  byParentStudents("student_id"),
				"Student": byOwnStudent("student_id"),
			},
		},
		{
			Name: "parents", Table: "parent", IDCol: "parent_id", IDPrefix: "par",
			Cols: []Col{
				{Name: "user_account_id", Kind: "text", Required: true},
				{Name: "name", Kind: "text", Required: true},
			},
			Derived: []Derived{
				{Name: "email", Expr: "(SELECT ua.email FROM user_account ua WHERE ua.user_account_id = parent.user_account_id)"},
			},
			ReadRoles: []string{"Parent"},
			Scope:     map[string]ScopeFn{"Parent": byOwnParent("parent_id")},
		},
		{
			Name: "teachers", Table: "teacher", IDCol: "teacher_id", IDPrefix: "tch",
			Cols: []Col{
				{Name: "user_account_id", Kind: "text", Required: true},
				{Name: "name", Kind: "text", Required: true},
				{Name: "phone", Kind: "text"},
				{Name: "email", Kind: "text"},
				{Name: "line_id", Kind: "text"},
			},
			ReadRoles: everyone,
		},
		{
			Name: "admins", Table: "admin", IDCol: "admin_id", IDPrefix: "adm",
			Cols: []Col{
				{Name: "user_account_id", Kind: "text", Required: true},
				{Name: "name", Kind: "text", Required: true},
				{Name: "phone", Kind: "text"},
				{Name: "email", Kind: "text"},
				{Name: "line_id", Kind: "text"},
			},
		},
		{
			Name: "parent-contacts", Table: "parent_contact", IDCol: "parent_contact_id", IDPrefix: "pct",
			Cols: []Col{
				{Name: "parent_id", Kind: "text", Required: true},
				{Name: "contact_type", Kind: "text", Enum: contactTypes, Required: true},
				{Name: "value", Kind: "text", Required: true},
			},
			ReadRoles: []string{"Parent"}, WriteRoles: []string{"Parent"},
			Scope: map[string]ScopeFn{"Parent": byOwnParent("parent_id")},
			Own:   ownParentRow,
		},
		{
			Name: "student-parents", Table: "student_parent", IDCol: "student_id", IDPrefix: "",
			Cols: []Col{
				{Name: "parent_id", Kind: "text", Required: true},
				{Name: "relationship_type", Kind: "text"},
			},
			ReadRoles: []string{"Parent"},
			Scope:     map[string]ScopeFn{"Parent": byOwnParent("parent_id")},
		},
		{
			Name: "notification-preferences", Table: "notification_preference", IDCol: "parent_id", IDPrefix: "",
			Cols: []Col{
				{Name: "check_in_alerts_enabled", Kind: "bool"},
				{Name: "credit_expiry_alerts_enabled", Kind: "bool"},
				{Name: "announcement_alerts_enabled", Kind: "bool"},
			},
			ReadRoles: []string{"Parent"}, WriteRoles: []string{"Parent"},
			Scope: map[string]ScopeFn{"Parent": byOwnParent("parent_id")},
			Own:   ownParentRow,
		},
		{
			Name: "classes", Table: "class", IDCol: "class_id", IDPrefix: "cls",
			Cols: []Col{
				{Name: "name", Kind: "text", Required: true},
				{Name: "description", Kind: "text"},
				{Name: "class_type", Kind: "text", Enum: classTypes, Required: true},
			},
			ReadRoles: everyone,
		},
		{
			Name: "class-sessions", Table: "class_session", IDCol: "session_id", IDPrefix: "ses",
			Cols: []Col{
				{Name: "class_id", Kind: "text", Required: true},
				{Name: "session_date", Kind: "text", Required: true},
				{Name: "start_time", Kind: "text", Required: true},
				{Name: "end_time", Kind: "text", Required: true},
				{Name: "duration_hours", Kind: "real"},
				{Name: "session_status", Kind: "text", Enum: sessionStatus},
			},
			ReadRoles: everyone, WriteRoles: []string{"Teacher"},
		},
		{
			Name: "enrollments", Table: "student_enrollment", IDCol: "enrollment_id", IDPrefix: "enr",
			Cols: []Col{
				{Name: "student_id", Kind: "text", Required: true},
				{Name: "class_id", Kind: "text", Required: true},
				{Name: "enrolled_date", Kind: "text", Required: true},
				{Name: "status", Kind: "text", Enum: enrollStatus},
			},
			ReadRoles: everyone,
			Scope: map[string]ScopeFn{
				"Parent":  byParentStudents("student_id"),
				"Student": byOwnStudent("student_id"),
			},
		},
		{
			Name: "attendance", Table: "attendance", IDCol: "attendance_id", IDPrefix: "att",
			Cols: []Col{
				{Name: "student_id", Kind: "text", Required: true},
				{Name: "session_id", Kind: "text", Required: true},
				{Name: "check_in_time", Kind: "text"},
				{Name: "check_out_time", Kind: "text"},
				{Name: "created_at", Kind: "text"},
				{Name: "updated_at", Kind: "text"},
			},
			ReadRoles: everyone, WriteRoles: []string{"Teacher"},
			Scope: map[string]ScopeFn{
				"Parent":  byParentStudents("student_id"),
				"Student": byOwnStudent("student_id"),
			},
		},
		{
			Name: "credit-packages", Table: "credit_package", IDCol: "credit_package_id", IDPrefix: "pkg",
			Cols: []Col{
				{Name: "class_id", Kind: "text", Required: true},
				{Name: "credit_amount", Kind: "real", Required: true},
				{Name: "standard_price", Kind: "real", Required: true},
				{Name: "validity_days", Kind: "int", Required: true},
			},
			ReadRoles: everyone,
		},
		{
			Name: "payments", Table: "payment", IDCol: "payment_id", IDPrefix: "pay",
			Cols: []Col{
				{Name: "student_id", Kind: "text", Required: true},
				{Name: "enrollment_id", Kind: "text"},
				{Name: "credit_package_id", Kind: "text"},
				{Name: "amount", Kind: "real", Required: true},
				{Name: "discount_amount", Kind: "real"},
				{Name: "final_amount", Kind: "real", Required: true},
				{Name: "payment_method", Kind: "text", Enum: payMethods, Required: true},
				{Name: "status", Kind: "text", Enum: payStatus},
				{Name: "payment_date", Kind: "text", Required: true},
				{Name: "reference_number", Kind: "text"},
			},
			ReadRoles: []string{"Parent", "Student"},
			Scope: map[string]ScopeFn{
				"Parent":  byParentStudents("student_id"),
				"Student": byOwnStudent("student_id"),
			},
		},
		{
			Name: "credit-transactions", Table: "credit_transaction", IDCol: "credit_transaction_id", IDPrefix: "ctx",
			Cols: []Col{
				{Name: "enrollment_id", Kind: "text", Required: true},
				{Name: "transaction_type", Kind: "text", Enum: creditTxTypes, Required: true},
				{Name: "amount", Kind: "real", Required: true},
				{Name: "expiry_date", Kind: "text"},
				{Name: "transaction_date", Kind: "text", Required: true},
				{Name: "payment_id", Kind: "text"},
				{Name: "attendance_id", Kind: "text"},
				{Name: "notes", Kind: "text"},
			},
			ReadRoles: []string{"Parent", "Student"},
			Scope: map[string]ScopeFn{
				"Parent": func(id *auth.Identity) (string, []any) {
					return `enrollment_id IN (SELECT enrollment_id FROM student_enrollment
						WHERE student_id IN (SELECT student_id FROM student_parent WHERE parent_id = ?))`, []any{id.ParentID}
				},
				"Student": func(id *auth.Identity) (string, []any) {
					return `enrollment_id IN (SELECT enrollment_id FROM student_enrollment WHERE student_id = ?)`, []any{id.StudentID}
				},
			},
		},
		{
			Name: "announcements", Table: "announcement", IDCol: "announcement_id", IDPrefix: "ann",
			Cols: []Col{
				{Name: "title", Kind: "text", Required: true},
				{Name: "body", Kind: "text", Required: true},
				{Name: "author_user_account_id", Kind: "text", Required: true},
				{Name: "posted_at", Kind: "text"},
				{Name: "has_attachment", Kind: "bool"},
			},
			ReadRoles: everyone, WriteRoles: []string{"Teacher"},
		},
		{
			Name: "tournaments", Table: "tournament", IDCol: "tournament_id", IDPrefix: "trn",
			Cols: []Col{
				{Name: "name", Kind: "text", Required: true},
				{Name: "tournament_status", Kind: "text", Enum: tournamentStat},
				{Name: "start_date", Kind: "text"},
				{Name: "end_date", Kind: "text"},
				{Name: "venue_name", Kind: "text"},
				{Name: "venue_address", Kind: "text"},
				{Name: "organizer_name", Kind: "text"},
				{Name: "registration_deadline", Kind: "text"},
				{Name: "early_bird_fee", Kind: "real"},
				{Name: "regular_fee", Kind: "real"},
				{Name: "max_participants", Kind: "int"},
				{Name: "registration_website_url", Kind: "text"},
				{Name: "registration_qr_code_image", Kind: "text"},
				{Name: "regulations_document_url", Kind: "text"},
			},
			ReadRoles: everyone,
		},
		{
			Name: "tournament-categories", Table: "tournament_category", IDCol: "tournament_category_id", IDPrefix: "tcat",
			Cols: []Col{
				{Name: "tournament_id", Kind: "text", Required: true},
				{Name: "name", Kind: "text", Required: true},
			},
			ReadRoles: everyone,
		},
		{
			Name: "tournament-registrations", Table: "tournament_registration", IDCol: "tournament_registration_id", IDPrefix: "treg",
			Cols: []Col{
				{Name: "tournament_id", Kind: "text", Required: true},
				{Name: "student_id", Kind: "text", Required: true},
				{Name: "participant_name", Kind: "text", Required: true},
				{Name: "participant_contact", Kind: "text"},
				{Name: "participant_date_of_birth", Kind: "text"},
				{Name: "tournament_category_id", Kind: "text"},
				{Name: "fide_rating", Kind: "real"},
				{Name: "fee_charged", Kind: "real"},
				{Name: "registered_at", Kind: "text"},
			},
			ReadRoles: []string{"Parent", "Student"}, WriteRoles: []string{"Parent"},
			Scope: map[string]ScopeFn{
				"Parent":  byParentStudents("student_id"),
				"Student": byOwnStudent("student_id"),
			},
			Own: ownChild,
		},
		{
			Name: "practice-activities", Table: "practice_activity", IDCol: "activity_id", IDPrefix: "act",
			Cols: []Col{
				{Name: "student_id", Kind: "text", Required: true},
				{Name: "activity_date", Kind: "text", Required: true},
				{Name: "minutes_practiced", Kind: "int"},
				{Name: "puzzles_completed", Kind: "int"},
				{Name: "points_earned", Kind: "int"},
				{Name: "streak_count", Kind: "int"},
			},
			ReadRoles: []string{"Parent", "Student"}, WriteRoles: []string{"Student"},
			Scope: map[string]ScopeFn{
				"Parent":  byParentStudents("student_id"),
				"Student": byOwnStudent("student_id"),
			},
			Own: ownChild,
		},
		{
			Name: "practice-settings", Table: "practice_settings", IDCol: "student_id", IDPrefix: "",
			Cols: []Col{
				{Name: "daily_screen_time_limit_minutes", Kind: "int", Required: true},
			},
			ReadRoles: []string{"Parent", "Student"}, WriteRoles: []string{"Parent"},
			Scope: map[string]ScopeFn{
				"Parent":  byParentStudents("student_id"),
				"Student": byOwnStudent("student_id"),
			},
			Own: ownChild,
		},
		{
			Name: "system-configuration", Table: "system_configuration", IDCol: "config_key", IDPrefix: "",
			Cols: []Col{
				{Name: "config_value", Kind: "text", Required: true},
			},
		},
	}
}
