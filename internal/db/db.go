// Package db opens the JTrax database — a local SQLite file in development
// or a remote Turso database in deployment — and applies embedded migrations.
package db

import (
	"database/sql"
	"embed"
	"fmt"
	"sort"
	"strings"

	_ "github.com/tursodatabase/libsql-client-go/libsql"
	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// Open opens the database named by dsn and applies pending migrations.
func Open(dsn string) (*sql.DB, error) {
	driver, conn, remote := resolve(dsn)
	d, err := sql.Open(driver, conn)
	if err != nil {
		return nil, err
	}
	if !remote {
		// modernc.org/sqlite is single-writer; serialize access to avoid SQLITE_BUSY.
		d.SetMaxOpenConns(1)
	}
	if err := migrate(d); err != nil {
		d.Close()
		return nil, err
	}
	return d, nil
}

// resolve maps a dsn to its driver. A libsql:// or https:// dsn is a remote
// Turso database reached over HTTP; anything else is a local SQLite file path.
func resolve(dsn string) (driver, conn string, remote bool) {
	if strings.HasPrefix(dsn, "libsql://") || strings.HasPrefix(dsn, "https://") {
		return "libsql", dsn, true
	}
	conn = fmt.Sprintf("file:%s?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)", dsn)
	return "sqlite", conn, false
}

// Redact strips the query string from a dsn — Turso carries its auth token
// there — so a dsn can be logged safely.
func Redact(dsn string) string {
	if i := strings.Index(dsn, "?"); i >= 0 {
		return dsn[:i] + "?<redacted>"
	}
	return dsn
}

// migrate applies every migrations/NNNN_*.sql not yet recorded, in filename order.
func migrate(d *sql.DB) error {
	if _, err := d.Exec(`CREATE TABLE IF NOT EXISTS schema_migration (
		filename   TEXT PRIMARY KEY,
		applied_at TEXT NOT NULL DEFAULT (datetime('now'))
	)`); err != nil {
		return err
	}
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	for _, name := range names {
		var done int
		if err := d.QueryRow(`SELECT COUNT(*) FROM schema_migration WHERE filename = ?`, name).Scan(&done); err != nil {
			return err
		}
		if done > 0 {
			continue
		}
		body, err := migrationFS.ReadFile("migrations/" + name)
		if err != nil {
			return err
		}
		tx, err := d.Begin()
		if err != nil {
			return err
		}
		// One statement per Exec on every driver: the remote libSQL protocol
		// requires it, and running it locally too keeps dev and deployment on
		// the same path.
		for _, stmt := range splitStatements(string(body)) {
			if _, err := tx.Exec(stmt); err != nil {
				tx.Rollback()
				return fmt.Errorf("migration %s: %w", name, err)
			}
		}
		if _, err := tx.Exec(`INSERT INTO schema_migration (filename) VALUES (?)`, name); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}
