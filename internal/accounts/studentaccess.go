// Access checks shared by school workflows.
package accounts

import (
	"database/sql"

	"github.com/Kusk24/jtrax-backend/internal/auth"
)

// CanSeeStudent reports whether the caller may read this pupil's record:
// the pupil themselves, a parent of theirs, or staff and teachers.
//
// Decided in a query rather than from the role alone, so a parent of one child
// cannot read another's by changing the id in the URL.
func CanSeeStudent(d *sql.DB, id *auth.Identity, studentID string) bool {
	if studentID == "" {
		return false
	}
	switch {
	case id.Role == "Student":
		return id.StudentID == studentID
	case IsStaff(id.Role) || id.Role == "Teacher":
		var n int
		_ = d.QueryRow(`SELECT COUNT(*) FROM student WHERE student_id = ?`, studentID).Scan(&n)
		return n > 0
	case id.Role == "Parent":
		var n int
		_ = d.QueryRow(`SELECT COUNT(*) FROM student_parent WHERE parent_id = ? AND student_id = ?`,
			id.ParentID, studentID).Scan(&n)
		return n > 0
	}
	return false
}
