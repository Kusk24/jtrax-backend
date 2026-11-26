// Persistence for registry resources. Hooks and their writes share one transaction.
package records

import (
	"database/sql"
	"strings"

	"github.com/Kusk24/jtrax-backend/internal/auth"
	"github.com/Kusk24/jtrax-backend/internal/db"
)

func (rs *Resource) insert(d *sql.DB, rowID string, row map[string]any) error {
	names := []string{rs.IDCol}
	marks := []string{"?"}
	args := []any{rowID}
	for name, v := range row {
		names = append(names, name)
		marks = append(marks, "?")
		args = append(args, v)
	}
	insert := "INSERT INTO " + rs.Table + " (" + strings.Join(names, ", ") + ") VALUES (" + strings.Join(marks, ", ") + ")"
	if rs.AfterWrite == nil && rs.AfterInsert == nil {
		_, err := d.Exec(insert, args...)
		return err
	}
	return db.InTransaction(d, func(tx *sql.Tx) error {
		if _, e := tx.Exec(insert, args...); e != nil {
			return e
		}
		if rs.AfterInsert != nil {
			if e := rs.AfterInsert(tx, rowID); e != nil {
				return e
			}
		}
		if rs.AfterWrite == nil {
			return nil
		}
		return rs.AfterWrite(tx, rowID)
	})
}

func (rs *Resource) update(d *sql.DB, id *auth.Identity, rowID string, existing, patch map[string]any) error {
	sets := []string{}
	args := []any{}
	for name, v := range patch {
		sets = append(sets, name+" = ?")
		args = append(args, v)
	}
	args = append(args, rowID)
	query := "UPDATE " + rs.Table + " SET " + strings.Join(sets, ", ") + " WHERE " + rs.IDCol + " = ?"
	if rs.AfterWrite == nil && rs.BeforeUpdate == nil {
		_, err := d.Exec(query, args...)
		return err
	}
	/* Re-run rather than adjust: moving a student to a longer class has
	   to change what the afternoon cost, and the hook works that out
	   from the row as it now stands. */
	return db.InTransaction(d, func(tx *sql.Tx) error {
		if rs.BeforeUpdate != nil {
			if e := rs.BeforeUpdate(tx, id, rowID, existing, patch); e != nil {
				return e
			}
		}
		if _, e := tx.Exec(query, args...); e != nil {
			return e
		}
		if rs.AfterWrite == nil {
			return nil
		}
		return rs.AfterWrite(tx, rowID)
	})
}

func (rs *Resource) delete(d *sql.DB, rowID string) (int64, error) {
	del := "DELETE FROM " + rs.Table + " WHERE " + rs.IDCol + " = ?"
	var affected int64
	var err error
	if rs.BeforeDelete == nil {
		var res sql.Result
		if res, err = d.Exec(del, rowID); err == nil {
			affected, _ = res.RowsAffected()
		}
	} else {
		/* What the row cost has to go with the row, and in the same breath:
		   credits refunded but the attendance still standing would be worse
		   than either alone. */
		err = db.InTransaction(d, func(tx *sql.Tx) error {
			if e := rs.BeforeDelete(tx, rowID); e != nil {
				return e
			}
			res, e := tx.Exec(del, rowID)
			if e != nil {
				return e
			}
			affected, _ = res.RowsAffected()
			return nil
		})
	}
	return affected, err
}
