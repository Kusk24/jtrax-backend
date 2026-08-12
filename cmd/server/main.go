// jtrax-backend — REST API for the JTrax portals: SQLite/Turso storage,
// session auth, and role-scoped CRUD over every ER-model entity under /api/v1.
//
// Configuration is entirely environment-driven; see .env.example.
package main

import (
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/Kusk24/jtrax-backend/internal/api"
	"github.com/Kusk24/jtrax-backend/internal/db"
	"github.com/Kusk24/jtrax-backend/internal/httpx"
)

func main() {
	dsn := firstSet(os.Getenv("DATABASE_URL"), os.Getenv("JTRAX_DB"), "jtrax.db")
	d, err := db.Open(dsn)
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	if err := seed(d, dsn); err != nil {
		log.Fatalf("seed database: %v", err)
	}

	origins := strings.Split(os.Getenv("ALLOWED_ORIGINS"), ",")
	if origins[0] == "" {
		origins = []string{"http://localhost:3000", "http://localhost:3001"}
	}
	handler := httpx.CORS(origins, api.NewHandler(d))

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	log.Printf("jtrax-backend listening on :%s (db %s)", port, db.Redact(dsn))
	log.Fatal(http.ListenAndServe(":"+port, handler))
}

// seed loads the development dataset. A local file database seeds itself so a
// fresh clone has usable data; a remote one must opt in with JTRAX_SEED=1,
// because seeding publishes accounts on a URL the internet can reach.
//
// JTRAX_SEED_PASSWORD sets the shared password for those accounts and is
// required when seeding a remote database — db.DevPassword is in the
// repository, so anyone could read it and sign in as the seeded admin.
func seed(d *sql.DB, dsn string) error {
	run, password, err := seedConfig(dsn, os.Getenv("JTRAX_SEED"), os.Getenv("JTRAX_SEED_PASSWORD"))
	if err != nil {
		return err
	}
	if !run {
		return nil
	}
	return db.Seed(d, password)
}

// seedConfig decides whether to seed and with which password. Split out from
// seed so the remote-safety rules can be tested without a database.
func seedConfig(dsn, flag, password string) (run bool, pw string, err error) {
	remote := strings.HasPrefix(dsn, "libsql://") || strings.HasPrefix(dsn, "https://")
	if remote && flag != "1" {
		return false, "", nil
	}
	if password == "" {
		if remote {
			return false, "", fmt.Errorf("JTRAX_SEED_PASSWORD is required when seeding a remote database")
		}
		password = db.DevPassword
	}
	return true, password, nil
}

// firstSet returns the first non-empty value.
func firstSet(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
