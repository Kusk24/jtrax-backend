// Two of the dashboard's week cards that need the server: games the office
// opened this week, and how many students are playing consistently. Read when
// the dashboard opens, not live. Staff only.
package api

import (
	"database/sql"
	"fmt"
	"net/http"
	"time"

	"github.com/Kusk24/jtrax-backend/internal/academytime"
	"github.com/Kusk24/jtrax-backend/internal/httpx"
)

const (
	// A student plays consistently when they did something on at least
	// consistentDays of the last consistentWindow days (today included).
	consistentDays   = 3
	consistentWindow = 7
)

type dashboardActivity struct {
	// Game rooms the office (admin or front desk) opened since Monday.
	GamesOpened int `json:"gamesOpened"`
	// Students active on at least ConsistentDays of the last WindowDays:
	// puzzles and daily challenges, practice, games against the computer, and
	// games in a game room.
	ConsistentPlayers int `json:"consistentPlayers"`
	ConsistentDays    int `json:"consistentDays"`
	WindowDays        int `json:"windowDays"`
}

// weekStart is the Monday of the academy week containing now, at midnight.
func weekStart(now time.Time) time.Time {
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	back := (int(day.Weekday()) + 6) % 7 // Monday = 0
	return day.AddDate(0, 0, -back)
}

func readDashboardActivity(d *sql.DB, now time.Time) (*dashboardActivity, error) {
	a := &dashboardActivity{ConsistentDays: consistentDays, WindowDays: consistentWindow}

	// Stored times are UTC without a zone ("2026-09-27 15:47:07").
	// The week of `now`, Monday to Sunday — bounded at both ends so a past
	// week counts only its own games.
	week := weekStart(now)
	monday := week.UTC().Format("2006-01-02 15:04:05")
	nextMonday := week.AddDate(0, 0, 7).UTC().Format("2006-01-02 15:04:05")
	if err := d.QueryRow(`SELECT COUNT(*) FROM game_room g
	                       JOIN user_account ua ON ua.user_account_id = g.created_by
	                      WHERE ua.role IN ('Admin', 'Receptionist')
	                        AND replace(replace(g.created_at, 'T', ' '), 'Z', '') >= ?
	                        AND replace(replace(g.created_at, 'T', ' '), 'Z', '') < ?`,
		monday, nextMonday).Scan(&a.GamesOpened); err != nil {
		return nil, err
	}

	// Every (student, academy day) with any play in the window. Timestamps are
	// shifted into the academy's zone before they are cut to a day.
	_, offset := now.Zone()
	shift := fmt.Sprintf("%+d seconds", offset)
	first := now.AddDate(0, 0, -(consistentWindow - 1)).Format(academytime.DayLayout)
	last := now.Format(academytime.DayLayout)
	err := d.QueryRow(`
		SELECT COUNT(*) FROM (
		  SELECT student_id FROM (
		    SELECT student_id, assigned_on AS day FROM puzzle_attempt WHERE solved = 1
		    UNION
		    SELECT student_id, activity_date FROM practice_activity
		    UNION
		    SELECT student_id, date(replace(COALESCE(ended_at, started_at), 'T', ' '), ?)
		      FROM solo_game WHERE COALESCE(ended_at, started_at) IS NOT NULL
		    UNION
		    SELECT s.student_id, date(replace(g.started_at, 'T', ' '), ?)
		      FROM game_room g
		      JOIN student s ON s.user_account_id IN (g.white_account_id, g.black_account_id)
		     WHERE g.started_at IS NOT NULL
		  )
		  WHERE day BETWEEN ? AND ?
		  GROUP BY student_id
		  HAVING COUNT(DISTINCT day) >= ?)`,
		shift, shift, first, last, consistentDays).Scan(&a.ConsistentPlayers)
	if err != nil {
		return nil, err
	}
	return a, nil
}

func handleDashboardActivity(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if requireStaff(d, w, r) == nil {
			return
		}
		// ?date=YYYY-MM-DD reads the week of that day (and the window
		// ending on it), for the dashboard's date picker; without it, today.
		now := academytime.Now()
		if day := r.URL.Query().Get("date"); day != "" {
			picked, err := time.ParseInLocation(academytime.DayLayout, day, academytime.Location())
			if err != nil {
				httpx.Error(w, http.StatusBadRequest, "date must be YYYY-MM-DD", nil)
				return
			}
			now = picked.Add(12 * time.Hour)
		}
		a, err := readDashboardActivity(d, now)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not read game activity", err)
			return
		}
		httpx.JSON(w, http.StatusOK, a)
	}
}

func mountDashboardActivity(mux *http.ServeMux, d *sql.DB) {
	mux.HandleFunc("GET /api/v1/dashboard/activity", handleDashboardActivity(d))
}
