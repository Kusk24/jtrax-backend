// Practice records and the streak derived from them.
//
// A streak used to be `student.streak_count`: a number the browser posted and
// nothing ever recomputed, so a pupil who had not practised since May still
// showed twelve days, and solving three puzzles moved it on screen and nowhere
// else. It is derived here instead, from the dates a pupil actually practised,
// and the client is never asked what it thinks the number is.
package api

import (
	"database/sql"
	"net/http"
	"time"

	"github.com/Kusk24/jtrax-backend/internal/httpx"
)

// streakGraceDays is how far back the most recent practice may be and still
// count as a live streak. One day, so a pupil who has not practised *yet
// today* still sees the streak they earned yesterday — it breaks at the end of
// the day they miss, not at midnight of the day they are still in.
//
// A missed day breaks it. The daily challenge is a home habit rather than a
// class register, and a flame that survives gaps is not a streak.
const streakGraceDays = 1

// practiceDays is how many days of history the portal's strip shows.
const practiceDays = 7

// currentStreak counts back from the pupil's most recent practice day, which
// must be today or yesterday, and stops at the first missing date.
func currentStreak(d *sql.DB, studentID string, today time.Time) (int, error) {
	rows, err := d.Query(
		`SELECT DISTINCT activity_date FROM practice_activity
		  WHERE student_id = ? AND activity_date <= ?
		  ORDER BY activity_date DESC LIMIT 400`,
		studentID, today.Format(dayLayout))
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	streak := 0
	// The day the next row has to be to continue the run. It starts at today
	// and is allowed to slip once, by streakGraceDays, before the first hit.
	want := today
	first := true
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return 0, err
		}
		day, err := time.Parse(dayLayout, raw)
		if err != nil {
			continue
		}
		if first {
			gap := int(want.Sub(day).Hours() / 24)
			if gap > streakGraceDays {
				return 0, nil // the run ended before today
			}
			want = day
			first = false
		} else if !day.Equal(want) {
			break // a missing day ends it
		}
		streak++
		want = want.AddDate(0, 0, -1)
	}
	return streak, rows.Err()
}

const dayLayout = "2006-01-02"

// recordPractice writes (or tops up) today's practice row for a pupil.
//
// Called from the puzzle grader when the day's set is finished, never from a
// request body: the numbers are what the server just watched happen.
func recordPractice(d *sql.DB, studentID, day string, puzzles, minutes, points int) error {
	_, err := d.Exec(`
		INSERT INTO practice_activity
		  (activity_id, student_id, activity_date, minutes_practiced, puzzles_completed, points_earned, streak_count)
		VALUES (?,?,?,?,?,?,0)
		ON CONFLICT(student_id, activity_date) DO UPDATE SET
		  puzzles_completed = excluded.puzzles_completed,
		  minutes_practiced = excluded.minutes_practiced,
		  points_earned     = excluded.points_earned`,
		newID("act"), studentID, day, minutes, puzzles, points)
	return err
}

type practiceDay struct {
	Date    string `json:"date"`
	Solved  int    `json:"solved"`
	Practis bool   `json:"practised"`
}

// handlePracticeSummary is everything the student home screen needs about
// practice: the streak, the last week as real days, and today's progress.
//
// The week is returned as dates rather than a count because the portal's strip
// used to light the first N of seven cells from the streak number alone, which
// drew a week the pupil had not lived.
func handlePracticeSummary(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := requireIdentity(d, w, r)
		if id == nil {
			return
		}
		studentID, ok := studentOf(w, id)
		if !ok {
			return
		}
		now := time.Now()
		streak, err := currentStreak(d, studentID, now)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not read practice", err)
			return
		}

		// One row per day for the strip, oldest first.
		solvedOn := map[string]int{}
		rows, err := d.Query(
			`SELECT activity_date, puzzles_completed FROM practice_activity
			  WHERE student_id = ? AND activity_date > ?`,
			studentID, now.AddDate(0, 0, -practiceDays).Format(dayLayout))
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not read practice", err)
			return
		}
		for rows.Next() {
			var day string
			var n int
			if err := rows.Scan(&day, &n); err == nil {
				solvedOn[day] = n
			}
		}
		rows.Close()

		days := make([]practiceDay, 0, practiceDays)
		for back := practiceDays - 1; back >= 0; back-- {
			key := now.AddDate(0, 0, -back).Format(dayLayout)
			n, did := solvedOn[key]
			days = append(days, practiceDay{Date: key, Solved: n, Practis: did})
		}

		// Today's set, so the card can show 2/3 without a second request.
		var todaySolved, todayTotal int
		_ = d.QueryRow(`SELECT COUNT(*), COALESCE(SUM(solved),0) FROM puzzle_attempt
		                WHERE student_id = ? AND assigned_on = ?`,
			studentID, now.Format(dayLayout)).Scan(&todayTotal, &todaySolved)

		httpx.JSON(w, http.StatusOK, map[string]any{
			"streak":      streak,
			"days":        days,
			"todaySolved": todaySolved,
			"todayTotal":  todayTotal,
		})
	}
}

func mountPractice(mux *http.ServeMux, d *sql.DB) {
	mux.HandleFunc("GET /api/v1/practice/summary", handlePracticeSummary(d))
}
