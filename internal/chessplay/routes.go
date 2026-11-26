// Route registration for chess activities.
package chessplay

import (
	"database/sql"

	"github.com/Kusk24/jtrax-backend/internal/httpx"
)

func RegisterRoutes(mux httpx.RouteRegistrar, d *sql.DB) {
	relay := mountGameRooms(mux, d)
	mountChallenges(mux, d, relay)
	mountPuzzles(mux, d)
	mountPuzzleBank(mux, d)
	mountPractice(mux, d)
	mountProgress(mux, d)
	mountHistory(mux, d)
	mountLichess(mux, d)
}
