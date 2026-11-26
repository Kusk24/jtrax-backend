// Database helpers shared by school workflows.
package db

import (
	"database/sql"
)

// InTransaction runs a write and its consequence as one unit. Resources with no hook
// keep the plain single-statement path — a transaction per row would be a cost
// paid by every resource for the sake of two.
func InTransaction(d *sql.DB, write func(tx *sql.Tx) error) error {
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	if err := write(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}
