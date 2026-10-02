// Who an announcement is for.
//
// The office addresses an announcement to every parent, to the parents of one
// or more classes, or to particular parents (see migration 0041). The parents
// it reaches are worked out once, when it is posted, and written to
// announcement_recipient — that list is both who is notified and who can read
// it afterwards.
package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
)

// audienceIDs reads audience_ids: a JSON array of class or parent ids.
func audienceIDs(v any) ([]string, error) {
	raw, _ := v.(string)
	if raw == "" {
		return nil, nil
	}
	var ids []string
	if err := json.Unmarshal([]byte(raw), &ids); err != nil {
		return nil, errors.New("audience_ids must be a JSON array of ids")
	}
	return ids, nil
}

// checkAnnouncementAudience: a class or parent audience has to name at least
// one class or parent — an announcement addressed to nobody is a mistake.
func checkAnnouncementAudience(row map[string]any) error {
	ids, err := audienceIDs(row["audience_ids"])
	if err != nil {
		return err
	}
	audience, _ := row["audience"].(string)
	if (audience == "classes" || audience == "parents") && len(ids) == 0 {
		return errors.New("choose at least one class or parent for this audience")
	}
	return nil
}

// resolveAnnouncementAudience turns the audience into the parents it reaches,
// the first time the announcement is written. Edits after that — a typo in
// the body — leave the recipients alone: the announcement was already sent to
// them, and re-reading a class's roster weeks later would quietly change who
// can see it.
func resolveAnnouncementAudience(tx *sql.Tx, announcementID string) error {
	var audience, idsJSON string
	if err := tx.QueryRow(
		`SELECT audience, audience_ids FROM announcement WHERE announcement_id = ?`,
		announcementID).Scan(&audience, &idsJSON); err != nil {
		return err
	}
	if audience == "all" {
		return nil
	}
	var already int
	if err := tx.QueryRow(
		`SELECT COUNT(*) FROM announcement_recipient WHERE announcement_id = ?`,
		announcementID).Scan(&already); err != nil {
		return err
	}
	if already > 0 {
		return nil
	}

	var query string
	switch audience {
	case "classes":
		// Parents of every child currently in one of the classes.
		query = `INSERT OR IGNORE INTO announcement_recipient (announcement_id, parent_id)
			SELECT DISTINCT ?, sp.parent_id FROM student_parent sp
			  JOIN student_enrollment e ON e.student_id = sp.student_id
			 WHERE e.status = 'Active'
			   AND e.class_id IN (SELECT value FROM json_each(?))`
	case "parents":
		query = `INSERT OR IGNORE INTO announcement_recipient (announcement_id, parent_id)
			SELECT ?, parent_id FROM parent
			 WHERE parent_id IN (SELECT value FROM json_each(?))`
	default:
		return nil
	}
	res, err := tx.Exec(query, announcementID, idsJSON)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return &ClientError{
			Status:  http.StatusUnprocessableEntity,
			Message: "no parents found for this audience — nobody would receive it",
		}
	}
	return nil
}

// announcementAccounts is who a posted announcement notifies: every parent
// account for "all", otherwise the accounts of the parents it was resolved to.
func announcementAccounts(d *sql.DB, announcementID, audience string) []string {
	var rows *sql.Rows
	var err error
	if audience == "" || audience == "all" {
		rows, err = d.Query(`SELECT user_account_id FROM user_account WHERE role = 'Parent'`)
	} else {
		rows, err = d.Query(`
			SELECT p.user_account_id FROM announcement_recipient r
			  JOIN parent p ON p.parent_id = r.parent_id
			 WHERE r.announcement_id = ? AND p.user_account_id IS NOT NULL`, announcementID)
	}
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
