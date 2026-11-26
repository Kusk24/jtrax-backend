// Family contact lookups for notifications.
package families

import (
	"database/sql"
)

// ParentAccountsOf returns the user_account_ids of a student's linked parents.
func ParentAccountsOf(d *sql.DB, studentID string) []string {
	rows, err := d.Query(
		`SELECT p.user_account_id FROM student_parent sp
		   JOIN parent p ON p.parent_id = sp.parent_id
		  WHERE sp.student_id = ? AND p.user_account_id IS NOT NULL
		    AND COALESCE(p.archived_at, '') = ''`, studentID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var uid string
		if err := rows.Scan(&uid); err == nil && uid != "" {
			ids = append(ids, uid)
		}
	}
	return ids
}

// ParentNamesOf is the display half of parentAccountsOf: the same join, but
// returning names for a person to read in the send preview rather than
// account ids to deliver to.
func ParentNamesOf(d *sql.DB, studentID string) []string {
	rows, err := d.Query(
		`SELECT p.name FROM student_parent sp
		   JOIN parent p ON p.parent_id = sp.parent_id
		  WHERE sp.student_id = ?`, studentID)
	if err != nil {
		return []string{}
	}
	defer rows.Close()
	names := []string{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err == nil && name != "" {
			names = append(names, name)
		}
	}
	return names
}

func StudentDisplayName(d *sql.DB, studentID string) string {
	var name sql.NullString
	d.QueryRow(`SELECT name FROM student WHERE student_id = ?`, studentID).Scan(&name)
	if name.Valid && name.String != "" {
		return name.String
	}
	return "Your child"
}
