// A pupil's points, level and progress for the student portal.
//
// Points are worked out from what the pupil has already done, not kept in a
// ledger: every puzzle solved, game finished and daily challenge completed is
// already recorded (puzzle_attempt, game_room, solo_game), so a count of those
// rows *is* the score. Nothing is written here, a pupil who played before the
// points existed starts with what they earned, and there is no second record
// that could drift from the first.
//
// The rule, as the academy set it:
//
//	a puzzle solved (daily or free play)   +5
//	a game finished (class, challenge, vs computer)   +10
//	the day's 3-puzzle daily challenge completed   +10
//
// A level is every pointsPerLevel points.
package api

import (
	"database/sql"
	"net/http"
	"time"

	"github.com/Kusk24/jtrax-backend/internal/academytime"
	"github.com/Kusk24/jtrax-backend/internal/httpx"
)

const (
	pointsPuzzle    = 5
	pointsGame      = 10
	pointsChallenge = 10
	pointsPerLevel  = 90
	// How much of the daily-challenge record the History tab shows.
	challengeHistoryDays = 60
	// How many days back the profile's streak calendar reaches.
	calendarDays = 28
)

type challengeDay struct {
	Date     string `json:"date"`
	Solved   int    `json:"solved"`
	Total    int    `json:"total"`
	Complete bool   `json:"complete"`
	// What the day earned: its puzzles, plus the challenge bonus when complete.
	Points int `json:"points"`
}

type progressView struct {
	Points struct {
		Total      int `json:"total"`
		Puzzles    int `json:"puzzles"`
		Games      int `json:"games"`
		Challenges int `json:"challenges"`
	} `json:"points"`
	Level struct {
		Level  int `json:"level"`
		Into   int `json:"into"`
		ToNext int `json:"toNext"`
		Size   int `json:"size"`
	} `json:"level"`
	Streak struct {
		Current int `json:"current"`
		Longest int `json:"longest"`
	} `json:"streak"`
	GamesPlayed         int `json:"gamesPlayed"`
	GamesWon            int `json:"gamesWon"`
	PuzzlesSolved       int `json:"puzzlesSolved"`
	ChallengesCompleted int `json:"challengesCompleted"`
	// The daily challenge, newest first, for History > Challenges.
	Challenges []challengeDay `json:"challenges"`
	// Days practised in the last calendarDays, for the streak calendar.
	PractisedDays []string `json:"practisedDays"`
}

// levelOf splits a points total into a level and the way to the next one.
func levelOf(points int) (level, into, toNext int) {
	level = points/pointsPerLevel + 1
	into = points % pointsPerLevel
	return level, into, pointsPerLevel - into
}

// longestStreak is the longest run of consecutive practice days on record.
func longestStreak(d *sql.DB, studentID string) (int, error) {
	rows, err := d.Query(`SELECT DISTINCT activity_date FROM practice_activity
	                       WHERE student_id = ? ORDER BY activity_date`, studentID)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	best, run := 0, 0
	var prev time.Time
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return 0, err
		}
		day, err := time.Parse(dayLayout, raw)
		if err != nil {
			continue
		}
		if run > 0 && day.Sub(prev).Hours() == 24 {
			run++
		} else {
			run = 1
		}
		prev = day
		if run > best {
			best = run
		}
	}
	return best, rows.Err()
}

// gameCounts is how many games the pupil has finished and won, on the
// academy's board and against the computer. A game still in play, or one the
// office cancelled, has no result and does not count.
func gameCounts(d *sql.DB, studentID string) (played, won int, err error) {
	err = d.QueryRow(`
		SELECT COUNT(*),
		       COALESCE(SUM(CASE WHEN (g.result = '1-0' AND g.white_account_id = s.user_account_id)
		                          OR (g.result = '0-1' AND g.black_account_id = s.user_account_id)
		                         THEN 1 ELSE 0 END), 0)
		FROM game_room g
		JOIN student s ON s.user_account_id IN (g.white_account_id, g.black_account_id)
		WHERE s.student_id = ? AND g.status = 'Finished' AND g.result IS NOT NULL`,
		studentID).Scan(&played, &won)
	if err != nil {
		return 0, 0, err
	}
	var soloPlayed, soloWon int
	err = d.QueryRow(`
		SELECT COUNT(*),
		       COALESCE(SUM(CASE WHEN (result = '1-0' AND student_side = 'white')
		                          OR (result = '0-1' AND student_side = 'black')
		                         THEN 1 ELSE 0 END), 0)
		FROM solo_game WHERE student_id = ? AND result IS NOT NULL`,
		studentID).Scan(&soloPlayed, &soloWon)
	if err != nil {
		return 0, 0, err
	}
	return played + soloPlayed, won + soloWon, nil
}

func handleStudentProgress(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := requireIdentity(d, w, r)
		if id == nil {
			return
		}
		studentID, ok := studentOf(w, id)
		if !ok {
			return
		}
		fail := func(err error) {
			httpx.Error(w, http.StatusInternalServerError, "could not read progress", err)
		}
		var p progressView

		if err := d.QueryRow(`SELECT COUNT(*) FROM puzzle_attempt WHERE student_id = ? AND solved = 1`,
			studentID).Scan(&p.PuzzlesSolved); err != nil {
			fail(err)
			return
		}

		played, won, err := gameCounts(d, studentID)
		if err != nil {
			fail(err)
			return
		}
		p.GamesPlayed, p.GamesWon = played, won

		// Every day with a daily set, newest first. The complete count is over
		// all of them; only the recent ones are listed.
		rows, err := d.Query(`SELECT assigned_on, COUNT(*), COALESCE(SUM(solved),0)
		                        FROM puzzle_attempt
		                       WHERE student_id = ? AND source = 'daily'
		                       GROUP BY assigned_on ORDER BY assigned_on DESC`, studentID)
		if err != nil {
			fail(err)
			return
		}
		p.Challenges = []challengeDay{}
		for rows.Next() {
			var c challengeDay
			if err := rows.Scan(&c.Date, &c.Total, &c.Solved); err != nil {
				rows.Close()
				fail(err)
				return
			}
			if c.Total < dailyCount {
				c.Total = dailyCount
			}
			c.Complete = c.Solved >= dailyCount
			c.Points = c.Solved * pointsPuzzle
			if c.Complete {
				p.ChallengesCompleted++
				c.Points += pointsChallenge
			}
			if len(p.Challenges) < challengeHistoryDays {
				p.Challenges = append(p.Challenges, c)
			}
		}
		rows.Close()

		p.Points.Puzzles = p.PuzzlesSolved * pointsPuzzle
		p.Points.Games = p.GamesPlayed * pointsGame
		p.Points.Challenges = p.ChallengesCompleted * pointsChallenge
		p.Points.Total = p.Points.Puzzles + p.Points.Games + p.Points.Challenges
		p.Level.Level, p.Level.Into, p.Level.ToNext = levelOf(p.Points.Total)
		p.Level.Size = pointsPerLevel

		now := academytime.Now()
		if p.Streak.Current, err = currentStreak(d, studentID, now); err != nil {
			fail(err)
			return
		}
		if p.Streak.Longest, err = longestStreak(d, studentID); err != nil {
			fail(err)
			return
		}
		// The current run is part of the record even if the rows disagree.
		if p.Streak.Current > p.Streak.Longest {
			p.Streak.Longest = p.Streak.Current
		}

		p.PractisedDays = []string{}
		rows, err = d.Query(`SELECT DISTINCT activity_date FROM practice_activity
		                      WHERE student_id = ? AND activity_date > ? AND activity_date <= ?
		                      ORDER BY activity_date`,
			studentID, now.AddDate(0, 0, -calendarDays).Format(dayLayout), now.Format(dayLayout))
		if err != nil {
			fail(err)
			return
		}
		for rows.Next() {
			var day string
			if err := rows.Scan(&day); err == nil {
				p.PractisedDays = append(p.PractisedDays, day)
			}
		}
		rows.Close()

		httpx.JSON(w, http.StatusOK, p)
	}
}

func mountProgress(mux *http.ServeMux, d *sql.DB) {
	mux.HandleFunc("GET /api/v1/student/progress", handleStudentProgress(d))
}
