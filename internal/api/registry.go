// The resource registry: one Resource definition per ER entity, with the
// role permissions and row scopes that implement the authorization rules.
package api

import (
	"database/sql"
	"slices"

	"github.com/Kusk24/jtrax-backend/internal/auth"
	"github.com/Kusk24/jtrax-backend/internal/records"
)

// Scope fragments reused across resources. Each binds one arg: the caller's
// parent_id or student_id.
func byParentStudents(col string) records.ScopeFn {
	return func(id *auth.Identity) (string, []any) {
		return col + " IN (SELECT student_id FROM student_parent WHERE parent_id = ?)", []any{id.ParentID}
	}
}

func byOwnStudent(col string) records.ScopeFn {
	return func(id *auth.Identity) (string, []any) { return col + " = ?", []any{id.StudentID} }
}

// notDeleted narrows an enrolment scope to the rows the office has not
// deleted — those are kept only for the console's history.
func notDeleted(scope records.ScopeFn) records.ScopeFn {
	return func(id *auth.Identity) (string, []any) {
		where, args := scope(id)
		return "(" + where + ") AND deleted_date IS NULL", args
	}
}

// notCancelled hides sessions the office has called off.
func notCancelled(*auth.Identity) (string, []any) { return "cancelled_at IS NULL", nil }

func byOwnParent(col string) records.ScopeFn {
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
	// Includes Pending for entries created by earlier registration workflows.
	registrationStat = []string{"Pending", "Approved", "Rejected", "Withdrawn"}
	arrivalStatus    = []string{"Pending", "Confirmed", "NotAttending"}
	contactTypes     = []string{"phone", "email", "line_id"}
)

// Registry lists the school resources in registration order.
func Registry() []*records.Resource {
	return slices.Concat(
		peopleResources(),
		classroomResources(),
		paymentResources(),
		announcementResources(),
		tournamentResources(),
		practiceResources(),
		configurationResources(),
	)
}
