// A tournament's status, from its dates.
//
// Upcoming before start_date, Ongoing from start_date through end_date (or
// just the start day when there is no end), Completed after. A tournament
// with no start date keeps whatever it has. The office can pin a status by
// hand (status_locked), and then the dates leave it alone.
//
// Applied whenever a tournament is saved, and on the entry sweeper's timer so
// a status moves on overnight without anyone opening the page.
package api

import (
	"database/sql"
	"log"
)

// statusByDates is the SQL for the status the dates give on `?` (a day).
const statusByDates = `CASE
	WHEN start_date > ? THEN 'Upcoming'
	WHEN COALESCE(NULLIF(end_date, ''), start_date) >= ? THEN 'Ongoing'
	ELSE 'Completed' END`

type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

// syncTournamentStatus brings every unlocked, dated tournament (or just
// `only`, when given) into line with its dates on `day`.
func syncTournamentStatus(d execer, day, only string) error {
	q := `UPDATE tournament SET tournament_status = ` + statusByDates + `
	       WHERE status_locked = 0 AND COALESCE(start_date, '') <> ''`
	args := []any{day, day}
	if only != "" {
		q += ` AND tournament_id = ?`
		args = append(args, only)
	}
	_, err := d.Exec(q, args...)
	return err
}

// tournamentStatusAfterWrite is the resource hook: a saved tournament gets
// the status its dates say, unless the office pinned one.
func tournamentStatusAfterWrite(tx *sql.Tx, id string) error {
	return syncTournamentStatus(tx, today(), id)
}

// SyncTournamentStatus runs the rule over every tournament as of `day`.
// Exported for tests.
func SyncTournamentStatus(d *sql.DB, day string) error {
	return syncTournamentStatus(d, day, "")
}

func logTournamentStatus(d *sql.DB) {
	if err := syncTournamentStatus(d, today(), ""); err != nil {
		log.Printf("tournament status: %v", err)
	}
}
