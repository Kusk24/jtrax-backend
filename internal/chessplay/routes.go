// Route registration for chess activities.
package chessplay

import (
	"database/sql"
	"github.com/gin-gonic/gin"
)

func RegisterRoutes(mux gin.IRoutes, d *sql.DB) {
	relay := mountGameRooms(mux, d)
	mountChallenges(mux, d, relay)
	mountPuzzles(mux, d)
	mountPuzzleBank(mux, d)
	mountPractice(mux, d)
	mountProgress(mux, d)
	mountHistory(mux, d)
	mountLichess(mux, d)
}
