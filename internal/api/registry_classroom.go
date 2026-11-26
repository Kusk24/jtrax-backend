// Classes, enrolment, attendance and bookings served by the shared CRUD API.
package api

import (
	"github.com/Kusk24/jtrax-backend/internal/classroom"
	"github.com/Kusk24/jtrax-backend/internal/payments"
	"github.com/Kusk24/jtrax-backend/internal/records"
)

func classroomResources() []*records.Resource {
	return []*records.Resource{
		{
			Name: "classes", Table: "class", IDCol: "class_id", IDPrefix: "cls",
			Cols: []records.Col{
				// Only the name is asked for. The rest describe a class rather
				// than identify it, and the office should not have to answer
				// three questions to write down that it teaches Beginners.
				{Name: "name", Kind: "text", Required: true},
				{Name: "description", Kind: "text"},
				// NOT NULL in the database, so a name-only class needs
				// something here; Group is what the console has always sent
				// when nobody chose.
				{Name: "class_type", Kind: "text", Enum: classTypes, Default: "Group"},
				// How the class is drawn and labelled. Both were offered by the
				// Academy screen for as long as it has existed and neither was
				// ever stored — the console re-derived them from class_type on
				// every render, so picking an icon changed nothing. No enum on
				// the icon: the names belong to the console's icon set, which
				// moves with the design. See 0022.
				{Name: "icon", Kind: "text"},
				{Name: "level", Kind: "text", Enum: classLevels},
				// Which certificate its hours count toward — JCA Starters,
				// Juniors or NXT — whatever the class type; blank for one
				// outside the three (certificates.go).
				{Name: "certificate_course", Kind: "text", Enum: classroom.CertificateCourses},
				// The usual price of one credit — what a new package or a custom
				// credit sale starts from; never binding (0067).
				{Name: "price_per_credit", Kind: "real"},
				// Set when the academy stops running this class. The row stays
				// so last term's attendance and receipts still name it; every
				// picker leaves it out. See 0020.
				{Name: "archived_at", Kind: "text"},
			},
			// Archived classes are served like any other: the console needs
			// them to put a name on an old enrolment, and hides them from the
			// places where one is chosen.
			ReadRoles: everyone,
		},
		{
			Name: "class-sessions", Table: "class_session", IDCol: "session_id", IDPrefix: "ses",
			Cols: []records.Col{
				{Name: "class_id", Kind: "text", Required: true},
				{Name: "session_date", Kind: "text", Required: true},
				{Name: "start_time", Kind: "text", Required: true},
				{Name: "end_time", Kind: "text", Required: true},
				{Name: "duration_hours", Kind: "real"},
				{Name: "session_status", Kind: "text", Enum: sessionStatus},
			},
			// When the office called it off (migration 0053). Read-only: only
			// the cancel endpoint sets it, since cancelling also refunds and
			// tells the families.
			Derived:   []records.Derived{{Name: "cancelled_at", Expr: "cancelled_at"}},
			ReadRoles: everyone, WriteRoles: []string{"Teacher"},
			// A cancelled class is the office's record, not a class anyone
			// goes to: teachers and families never see it.
			Scope: map[string]records.ScopeFn{
				"Teacher": notCancelled,
				"Parent":  notCancelled,
				"Student": notCancelled,
			},
			// The form collects a start and an end and never worked out the
			// difference, so every session staff created carried a NULL
			// length — and a length is what an hour of class costs.
			AfterWrite: payments.StoreSessionHours,
			// Bookings go with the class; attendance has its own refunds.
			BeforeDelete: classroom.DropSessionBookings,
		},
		{
			Name: "enrollments", Table: "student_enrollment", IDCol: "enrollment_id", IDPrefix: "enr",
			Cols: []records.Col{
				{Name: "student_id", Kind: "text", Required: true},
				{Name: "class_id", Kind: "text", Required: true},
				{Name: "enrolled_date", Kind: "text", Required: true},
				{Name: "status", Kind: "text", Enum: enrollStatus},
				// The course this child moved out of to be here. Null on an
				// enrolment made directly, which is most of them. It answers
				// the question a Withdrawn row cannot: whether they left the
				// academy or moved up.
				{Name: "moved_from_class_id", Kind: "text"},
				// The office's own note on this enrolment (migration 0042).
				{Name: "notes", Kind: "text"},
				// The day it stopped being Active (migration 0043).
				{Name: "ended_date", Kind: "text"},
				// Set when the office deletes it (migration 0044). The row is
				// kept for the student's history; the family does not see it.
				{Name: "deleted_date", Kind: "text"},
			},
			ReadRoles: everyone,
			Scope: map[string]records.ScopeFn{
				"Parent":  notDeleted(byParentStudents("student_id")),
				"Student": notDeleted(byOwnStudent("student_id")),
			},
		},
		{
			Name: "attendance", Table: "attendance", IDCol: "attendance_id", IDPrefix: "att",
			Cols: []records.Col{
				{Name: "student_id", Kind: "text", Required: true},
				{Name: "session_id", Kind: "text", Required: true},
				{Name: "check_in_time", Kind: "text"},
				{Name: "check_out_time", Kind: "text"},
				{Name: "created_at", Kind: "text"},
				{Name: "updated_at", Kind: "text"},
			},
			ReadRoles: everyone, WriteRoles: []string{"Teacher"},
			Scope: map[string]records.ScopeFn{
				"Parent":  byParentStudents("student_id"),
				"Student": byOwnStudent("student_id"),
			},
			// One credit is one hour: being at a session spends what the
			// session lasts. Here rather than in the console because the front
			// desk, the teacher's roster and Class History all write these
			// rows, and three clients would keep three versions of the rule.
			AfterInsert:  classroom.AttendanceAfterInsert,
			AfterWrite:   payments.ChargeAttendance,
			BeforeDelete: payments.RefundAttendance,
		},
		{
			// Who will be in a class that has not started (migration 0060).
			// Free until the start, when classstart.go checks each one in.
			Name: "session-bookings", Table: "session_booking", IDCol: "booking_id", IDPrefix: "bkg",
			Cols: []records.Col{
				{Name: "session_id", Kind: "text", Required: true},
				{Name: "student_id", Kind: "text", Required: true},
			},
			Derived:   []records.Derived{{Name: "booked_at", Expr: "booked_at"}, {Name: "failed_reason", Expr: "failed_reason"}},
			ReadRoles: everyone, WriteRoles: []string{"Teacher"},
			Scope: map[string]records.ScopeFn{
				"Parent":  byParentStudents("student_id"),
				"Student": byOwnStudent("student_id"),
			},
			AfterInsert: classroom.RefuseBooking,
		},
	}
}
