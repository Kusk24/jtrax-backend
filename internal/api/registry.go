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

// notDeleted narrows an enrolment scope to the rows the office has not
// deleted — those are kept only for the console's history.
func notDeleted(scope ScopeFn) ScopeFn {
	return func(id *auth.Identity) (string, []any) {
		where, args := scope(id)
		return "(" + where + ") AND deleted_date IS NULL", args
	}
}

// notCancelled hides sessions the office has called off.
func notCancelled(*auth.Identity) (string, []any) { return "cancelled_at IS NULL", nil }

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
	everyone      = []string{"Teacher", "Parent", "Student"}
	sessionStatus = []string{"Scheduled", "Ongoing", "Completed"}
	enrollStatus  = []string{"Active", "Completed", "Withdrawn"}
	// How a course is taught: one-to-one or a group. "Master" was a level
	// and is now one (0066).
	classTypes = []string{"Private", "Group"}
	// A course's level, apart from its type (0066).
	classLevels = []string{"Beginner", "Intermediate", "Advanced"}
	payMethods  = []string{"CreditCard", "BankTransfer", "Cash", "PromptPay"}
	// Pending is not revenue; the console totals only Paid. There are no
	// refunds — fees are non-refundable. Cancelled: a tournament fee still
	// owed when its place was released at closing (0059).
	payStatus      = []string{"Paid", "Pending", "Cancelled"}
	creditTxTypes  = []string{"purchase", "consumption", "manual_adjustment"}
	tournamentStat = []string{"Upcoming", "Ongoing", "Completed"}
	// Public sign-ups arrive Pending; staff entry has always meant Approved.
	registrationStat = []string{"Pending", "Approved", "Rejected", "Withdrawn"}
	arrivalStatus    = []string{"Pending", "Confirmed", "NotAttending"}
	contactTypes     = []string{"phone", "email", "line_id"}
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
			Cols: []Col{
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
			Derived:   []Derived{{Name: "cancelled_at", Expr: "cancelled_at"}},
			ReadRoles: everyone, WriteRoles: []string{"Teacher"},
			// A cancelled class is the office's record, not a class anyone
			// goes to: teachers and families never see it.
			Scope: map[string]ScopeFn{
				"Teacher": notCancelled,
				"Parent":  notCancelled,
				"Student": notCancelled,
			},
			// The form collects a start and an end and never worked out the
			// difference, so every session staff created carried a NULL
			// length — and a length is what an hour of class costs.
			AfterWrite: storeSessionHours,
			// Bookings go with the class; attendance has its own refunds.
			BeforeDelete: dropSessionBookings,
		},
		{
			Name: "enrollments", Table: "student_enrollment", IDCol: "enrollment_id", IDPrefix: "enr",
			Cols: []Col{
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
			Scope: map[string]ScopeFn{
				"Parent":  notDeleted(byParentStudents("student_id")),
				"Student": notDeleted(byOwnStudent("student_id")),
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
			// One credit is one hour: being at a session spends what the
			// session lasts. Here rather than in the console because the front
			// desk, the teacher's roster and Class History all write these
			// rows, and three clients would keep three versions of the rule.
			AfterInsert:  attendanceAfterInsert,
			AfterWrite:   chargeAttendance,
			BeforeDelete: refundAttendance,
		},
		{
			// Who will be in a class that has not started (migration 0060).
			// Free until the start, when classstart.go checks each one in.
			Name: "session-bookings", Table: "session_booking", IDCol: "booking_id", IDPrefix: "bkg",
			Cols: []Col{
				{Name: "session_id", Kind: "text", Required: true},
				{Name: "student_id", Kind: "text", Required: true},
			},
			Derived:   []Derived{{Name: "booked_at", Expr: "booked_at"}, {Name: "failed_reason", Expr: "failed_reason"}},
			ReadRoles: everyone, WriteRoles: []string{"Teacher"},
			Scope: map[string]ScopeFn{
				"Parent":  byParentStudents("student_id"),
				"Student": byOwnStudent("student_id"),
			},
			AfterInsert: refuseBooking,
		},
		{
			Name: "credit-packages", Table: "credit_package", IDCol: "credit_package_id", IDPrefix: "pkg",
			Cols: []Col{
				{Name: "class_id", Kind: "text", Required: true},
				{Name: "credit_amount", Kind: "real", Required: true},
				{Name: "standard_price", Kind: "real", Required: true},
				// Optional since 0035: a package the office never wants to
				// expire — a founding rate, a free trial — has nothing
				// truthful to put here. Absent reads as "never", the same as
				// 0 always has downstream (grantPurchasedCredits, expiryFrom).
				{Name: "validity_days", Kind: "int"},
				// Set when the academy stops selling this package. Payments
				// point at it and a receipt has to keep saying what it bought,
				// so the row stays and only the till forgets. See 0021.
				{Name: "archived_at", Kind: "text"},
			},
			ReadRoles: everyone,
			// Blank is "never expires"; 0 is refused rather than read as the
			// same, because "0 days" says the opposite to whoever reads it.
			Check: checkPackageValidity,
		},
		{
			Name: "payments", Table: "payment", IDCol: "payment_id", IDPrefix: "pay",
			Cols: []Col{
				// Not required: a payment outlives the student it was taken
				// for, and a detached one carries the names below instead.
				{Name: "student_id", Kind: "text"},
				{Name: "enrollment_id", Kind: "text"},
				{Name: "credit_package_id", Kind: "text"},
				// Credits this payment bought (migration 0054): a package's
				// count, or a custom number the desk typed in.
				{Name: "credit_amount", Kind: "real"},
				// Snapshots of who and what, written when the money changed
				// hands. Renaming a class later must not rewrite old receipts.
				{Name: "student_name", Kind: "text"},
				{Name: "class_name", Kind: "text"},
				{Name: "parent_name", Kind: "text"},
				{Name: "amount", Kind: "real", Required: true},
				{Name: "discount_amount", Kind: "real"},
				{Name: "final_amount", Kind: "real", Required: true},
				{Name: "payment_method", Kind: "text", Enum: payMethods, Required: true},
				{Name: "status", Kind: "text", Enum: payStatus},
				{Name: "payment_date", Kind: "text", Required: true},
				{Name: "reference_number", Kind: "text"},
				// Set when the money is a tournament entry fee rather than a
				// credit package. The portal reads it to tell a family which
				// of their registrations is still owed for.
				{Name: "tournament_registration_id", Kind: "text"},
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
				// Not required since 0026: a credit with no enrolment is one
				// the child holds and has not spent anywhere yet, which is
				// what is left when their last enrolment is deleted.
				{Name: "enrollment_id", Kind: "text"},
				// Who the hours belong to, and what they were bought for. The
				// enrolment used to answer both by proxy, which worked only
				// while there was one.
				{Name: "student_id", Kind: "text"},
				{Name: "class_id", Kind: "text"},
				{Name: "transaction_type", Kind: "text", Enum: creditTxTypes, Required: true},
				{Name: "amount", Kind: "real", Required: true},
				{Name: "expiry_date", Kind: "text"},
				{Name: "transaction_date", Kind: "text", Required: true},
				{Name: "payment_id", Kind: "text"},
				{Name: "attendance_id", Kind: "text"},
				{Name: "notes", Kind: "text"},
			},
			ReadRoles: []string{"Parent", "Student"},
			// Reached by the enrolment *or* by the student on the row. Since
			// 0026 a credit can outlive its enrolment, and matching only
			// through `student_enrollment` would have made a family's
			// remaining balance disappear from their own portal the moment the
			// office tidied away the course it was bought for. `student_id` is
			// null on nothing the backfill could resolve, so the first arm
			// still carries the rows written before that migration.
			Scope: map[string]ScopeFn{
				"Parent": func(id *auth.Identity) (string, []any) {
					return `(enrollment_id IN (SELECT enrollment_id FROM student_enrollment
						WHERE student_id IN (SELECT student_id FROM student_parent WHERE parent_id = ?))
						OR student_id IN (SELECT student_id FROM student_parent WHERE parent_id = ?))`,
						[]any{id.ParentID, id.ParentID}
				},
				"Student": func(id *auth.Identity) (string, []any) {
					return `(enrollment_id IN (SELECT enrollment_id FROM student_enrollment WHERE student_id = ?)
						OR student_id = ?)`, []any{id.StudentID, id.StudentID}
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
				// Who it is for (see 0041): every parent, the parents of some
				// classes, or some parents by name — audience_ids holds the
				// classes or parents picked, as a JSON array.
				{Name: "audience", Kind: "text", Enum: []string{"all", "classes", "parents"}},
				{Name: "audience_ids", Kind: "text"},
			},
			ReadRoles: everyone, WriteRoles: []string{"Teacher"},
			Check: checkAnnouncementAudience,
			// Resolved once, when it is posted: see resolveAnnouncementAudience.
			AfterWrite: resolveAnnouncementAudience,
			Scope: map[string]ScopeFn{
				// Everything addressed to every parent, and whatever was
				// addressed to this one.
				"Parent": func(id *auth.Identity) (string, []any) {
					return `(audience = 'all' OR announcement_id IN
						(SELECT announcement_id FROM announcement_recipient WHERE parent_id = ?))`,
						[]any{id.ParentID}
				},
				// Announcements are for families; students are not an audience.
				"Student": func(id *auth.Identity) (string, []any) {
					return `1 = 0`, nil
				},
			},
		},
		{
			Name: "tournaments", Table: "tournament", IDCol: "tournament_id", IDPrefix: "trn",
			Cols: []Col{
				{Name: "name", Kind: "text", Required: true},
				{Name: "tournament_status", Kind: "text", Enum: tournamentStat},
				// Set when the office picks a status by hand (migration
				// 0056); otherwise the dates decide (tournamentstatus.go).
				{Name: "status_locked", Kind: "bool"},
				{Name: "start_date", Kind: "text"},
				{Name: "end_date", Kind: "text"},
				{Name: "venue_name", Kind: "text"},
				{Name: "venue_address", Kind: "text"},
				// A Google Maps search link built from venue_name at creation
				// time — see 0037. Not derived on read, so it survives the
				// venue name later being edited to something the link no
				// longer matches without silently drifting.
				{Name: "venue_map_url", Kind: "text"},
				{Name: "organizer_name", Kind: "text"},
				{Name: "registration_deadline", Kind: "text"},
				{Name: "early_bird_fee", Kind: "real"},
				// The day early bird ends — without it the fee above was
				// never chargeable (see 0029).
				{Name: "early_bird_deadline", Kind: "text"},
				{Name: "regular_fee", Kind: "real"},
				{Name: "max_participants", Kind: "int"},
				{Name: "registration_website_url", Kind: "text"},
				{Name: "registration_qr_code_image", Kind: "text"},
				{Name: "regulations_document_url", Kind: "text"},
				// Days before the start to ask entrants whether they are
				// coming (migration 0055). Empty or 0: no reminder.
				{Name: "arrival_reminder_days", Kind: "int"},
				// Opt-in, and staff-only to change: turning it on publishes
				// children's names and scores to anyone with the link.
				{Name: "results_public", Kind: "bool"},
				// Opt-in for the same reason: it opens a write endpoint to
				// anyone with the link.
				{Name: "public_registration", Kind: "bool"},
				{Name: "student_discount_pct", Kind: "int"},
				// Which reductions a JCA student gets at this event — the
				// discount above, the early-bird price, both or neither (0035).
				{Name: "student_gets_discount", Kind: "bool"},
				{Name: "student_gets_early_bird", Kind: "bool"},
				// The chess-results.com event this tournament is published as,
				// when the standings are somebody else's to author. Read here so
				// the console's list can say which events follow an arbiter.
				{Name: "chess_results_id", Kind: "int"},
				// Saved by the create wizard before the organiser has reviewed
				// it, and taken live only by /publish (see tournamentdraft.go).
				{Name: "draft", Kind: "bool"},
			},
			Derived: []Derived{
				// Whether the organiser uploaded a banner; without one the
				// pages draw their own (see banner.go).
				{Name: "has_banner", Roles: everyone,
					Expr: "EXISTS(SELECT 1 FROM tournament_banner b WHERE b.tournament_id = tournament.tournament_id)"},
				// Whether a regulation file was uploaded, so the parent portal
				// links it only when there is one to open (regulation.go).
				{Name: "has_regulation", Roles: everyone,
					Expr: "EXISTS(SELECT 1 FROM tournament_regulation g WHERE g.tournament_id = tournament.tournament_id)"},
			},
			AfterWrite: tournamentStatusAfterWrite,
			ReadRoles:  everyone,
			// A draft is the organiser's until it is published.
			Scope: map[string]ScopeFn{
				"Teacher": notDraft,
				"Parent":  notDraft,
				"Student": notDraft,
			},
			// What a JCA student is charged today, from the same rule the
			// server charges by — so the portals show a price rather than
			// work one out (see pricing.go).
			Decorate: withStudentFee,
		},
		{
			Name: "tournament-categories", Table: "tournament_category", IDCol: "tournament_category_id", IDPrefix: "tcat",
			Cols: []Col{
				{Name: "tournament_id", Kind: "text", Required: true},
				{Name: "name", Kind: "text", Required: true},
			},
			ReadRoles:    everyone,
			BeforeDelete: releaseCategoryEntrants,
		},
		{
			Name: "tournament-registrations", Table: "tournament_registration", IDCol: "tournament_registration_id", IDPrefix: "treg",
			Cols: []Col{
				{Name: "tournament_id", Kind: "text", Required: true},
				// No longer required: a member of the public registering for an
				// open event has no student record, and NULL is what says so.
				{Name: "student_id", Kind: "text"},
				{Name: "participant_name", Kind: "text", Required: true},
				{Name: "participant_contact", Kind: "text"},
				{Name: "participant_date_of_birth", Kind: "text"},
				{Name: "tournament_category_id", Kind: "text"},
				{Name: "fide_rating", Kind: "real"},
				{Name: "fee_charged", Kind: "real"},
				{Name: "registered_at", Kind: "text"},
				{Name: "status", Kind: "text", Enum: registrationStat},
				{Name: "source", Kind: "text"},
				{Name: "contact_email", Kind: "text"},
				{Name: "contact_phone", Kind: "text"},
				{Name: "fee_quoted", Kind: "real"},
				{Name: "student_discount_applied", Kind: "bool"},
				// What the family said about their own child. Medical notes are
				// for the day; remarks are a request for the office.
				{Name: "medical_notes", Kind: "text"},
				{Name: "remarks", Kind: "text"},
				// Called across the hall and printed on the pairing card.
				{Name: "nickname", Kind: "text"},
				// As claimed on the entry form. Kept alongside the date of
				// birth rather than derived from it: the interesting case for
				// an age-limited group is when the two disagree.
				{Name: "participant_age", Kind: "int"},
				// As printed on a Thai ID card; a passport holder has none.
				{Name: "participant_name_th", Kind: "text"},
				// Which chess-results player this entry is, when staff picked
				// one because the names did not match (0052). Empty means the
				// console matches by name.
				{Name: "results_section_id", Kind: "int"},
				{Name: "results_player_name", Kind: "text"},
				// Whether they are coming (migration 0055): Pending until they
				// answer the reminder, or the desk records a phone call.
				{Name: "arrival_status", Kind: "text", Enum: arrivalStatus},
			},
			// terms_accepted_at is readable and not writable. It is a record of
			// consent, and a consent record staff can set by hand is one that
			// cannot be relied on for the question it exists to answer.
			Derived: []Derived{
				{Name: "terms_accepted_at", Expr: "terms_accepted_at"},
				{Name: "arrival_reminded_at", Expr: "arrival_reminded_at"},
				{Name: "arrival_answered_at", Expr: "arrival_answered_at"},
				// What the ID card scan read, beside what was submitted —
				// evidence for staff checking an age group, so not editable.
				{Name: "ocr_name", Expr: "ocr_name"},
				{Name: "ocr_date_of_birth", Expr: "ocr_date_of_birth"},
				{Name: "id_document_type", Expr: "id_document_type"},
				// The early-bird and closing-date rules' own record of what
				// they did (entryrules.go).
				{Name: "early_bird_applied", Expr: "early_bird_applied"},
				{Name: "early_bird_lapsed_at", Expr: "early_bird_lapsed_at"},
				{Name: "released_at", Expr: "released_at"},
			},
			// Staff write here; a parent enters their child through
			// `tournaments/{id}/entries`, which prices the entry itself. This
			// door used to take the fee and status from the parent's own
			// request, so a family could register at a price they chose.
			ReadRoles: []string{"Parent", "Student"},
			Scope: map[string]ScopeFn{
				"Parent":  byParentStudents("student_id"),
				"Student": byOwnStudent("student_id"),
			},
			Check:      checkRegistrationNotes,
			AfterWrite: syncRegistrationPayment,
		},
		{
			Name: "practice-activities", Table: "practice_activity", IDCol: "activity_id", IDPrefix: "act",
			Cols: []Col{
				{Name: "student_id", Kind: "text", Required: true},
				{Name: "activity_date", Kind: "text", Required: true},
				{Name: "minutes_practiced", Kind: "int"},
				{Name: "puzzles_completed", Kind: "int"},
				{Name: "points_earned", Kind: "int"},
				// `streak_count` is deliberately not writable: the streak is
				// derived from these dates now (see practice.go), so a stored
				// number could only disagree with the truth.
			},
			// Pupils read their own practice but no longer write it — the
			// server records a solve when it grades one. The portal used to
			// post its own row, including the streak it thought it had.
			ReadRoles: []string{"Parent", "Student"}, WriteRoles: nil,
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
			// Authoring only. Students never touch this resource — they are
			// served by /puzzles/daily, which omits the solution. Teachers can
			// read and write so they can set the position they taught on
			// Tuesday rather than a random fork.
			Name: "puzzles", Table: "puzzle", IDCol: "puzzle_id", IDPrefix: "pzl",
			Cols: []Col{
				{Name: "fen", Kind: "text", Required: true},
				{Name: "moves", Kind: "text", Required: true},
				{Name: "rating", Kind: "int"},
				{Name: "themes", Kind: "text"},
				{Name: "source", Kind: "text", Enum: puzzleSources},
				{Name: "created_by", Kind: "text"},
			},
			ReadRoles: []string{"Teacher"}, WriteRoles: []string{"Teacher"},
			Check: checkPuzzle,
		},
		{
			Name: "system-configuration", Table: "system_configuration", IDCol: "config_key", IDPrefix: "",
			Cols: []Col{
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
