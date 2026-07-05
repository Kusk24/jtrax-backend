// jtrax-backend — Go scaffold only. No framework or architecture is chosen
// yet; everything beyond /health waits for the system design to be finished.
package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
)

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	port := os.Getenv("PORT")
	if port == "" {
		port = "3000"
	}
	log.Printf("jtrax-backend listening on http://localhost:%s", port)
	log.Fatal(http.ListenAndServe(":"+port, mux))
}
