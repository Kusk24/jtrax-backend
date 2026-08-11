// jtrax-backend — REST API for the JTrax portals: SQLite storage, session
// auth, and role-scoped CRUD over every ER-model entity under /api/v1.
package main

import (
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/Kusk24/jtrax-backend/internal/api"
	"github.com/Kusk24/jtrax-backend/internal/db"
	"github.com/Kusk24/jtrax-backend/internal/httpx"
)

func main() {
	dbPath := os.Getenv("JTRAX_DB")
	if dbPath == "" {
		dbPath = "jtrax.db"
	}
	d, err := db.Open(dbPath)
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	if err := db.Seed(d); err != nil {
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
	log.Printf("jtrax-backend listening on http://localhost:%s (db %s)", port, dbPath)
	log.Fatal(http.ListenAndServe(":"+port, handler))
}
