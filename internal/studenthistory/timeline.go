// Package studenthistory reads the combined solo game, board game, and puzzle timeline.
package studenthistory

import (
	"database/sql"
	"sort"
	"strconv"
	"time"

	"github.com/Kusk24/jtrax-backend/internal/academytime"
	"github.com/Kusk24/jtrax-backend/internal/game"
)

// Entry is one thing a pupil did, whatever kind it was.
type Entry struct {
	// "solo", "room" or "puzzle".
	Kind string `json:"kind"`
	ID   string `json:"id"`
	At   string `json:"at"`
	// Who or what they played: an opponent's name, a puzzle's rating.
	Against string `json:"against"`
	Result  string `json:"result,omitempty"`
	Reason  string `json:"reason,omitempty"`
	Moves   int    `json:"moves,omitempty"`
	// The academy's calendar day it happened on. At is a UTC timestamp for
	// games, so a game at 6am Bangkok would otherwise land on yesterday.
	Day string `json:"day"`
	// For a game: when it began, so the console can say how long it took.
	// For a puzzle: when it was opened.
	StartedAt string `json:"startedAt,omitempty"`
	// For a puzzle: "daily" (the day's set) or "free" (free play).
	Source string `json:"source,omitempty"`
	// Set on a board game that was also a real game on lichess.org, with the
	// id to open it there. This is the only kind of play that counts on a
	// pupil's Lichess rating.
	LichessGameID string `json:"lichessGameId,omitempty"`

	// For a game, what the student portal's History shows about it.
	// Opponent is the other player's name, or the computer opponent's key.
	Opponent string `json:"opponent,omitempty"`
	// "white" or "black": the pupil's side.
	Side string `json:"side,omitempty"`
	// "5+0" when a clock was chosen; empty for an untimed game.
	TimeControl string `json:"timeControl,omitempty"`
	// "computer", "class" (the office handed it out) or "challenge" (one
	// pupil invited another).
	GameType string `json:"gameType,omitempty"`
	// What it earned towards the student portal's points: a finished game is
	// worth the same whoever won, a puzzle only when solved.
	Points int `json:"points,omitempty"`
}

// clockLabel is a time control as the portal shows it, "10+5".
func clockLabel(limit, increment int) string {
	return strconv.Itoa(limit/60) + "+" + strconv.Itoa(increment)
}

// Load reads the timeline for a student whose visibility the caller has established.
func Load(d *sql.DB, studentID string) ([]Entry, error) {
	out := []Entry{}

	// Against the computer.
	rows, err := d.Query(`SELECT solo_game_id, ended_at, opponent,
                             COALESCE(result,''), COALESCE(result_reason,''), move_count,
                             COALESCE(started_at,''), student_side
                      FROM solo_game WHERE student_id = ?
                      ORDER BY ended_at DESC LIMIT 200`, studentID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		e := Entry{Kind: "solo"}
		if err := rows.Scan(&e.ID, &e.At, &e.Against, &e.Result, &e.Reason, &e.Moves, &e.StartedAt, &e.Side); err == nil {
			e.Day = academyDayOf(e.At)
			e.Opponent, e.GameType = e.Against, "computer"
			if e.Result != "" {
				e.Points = game.FinishedGamePoints
			}
			out = append(out, e)
		}
	}
	rows.Close()

	// On the academy's board. The pupil's account is one of the two seats;
	// a room they only watched is not their game.
	rows, err = d.Query(`SELECT g.game_room_id, COALESCE(g.ended_at, g.created_at),
                            COALESCE(g.label,''), COALESCE(g.result,''),
                            COALESCE(g.result_reason,''), COALESCE(g.lichess_game_id,''),
                            (SELECT COUNT(*) FROM game_move m WHERE m.game_room_id = g.game_room_id),
                            COALESCE(g.started_at,''),
                            CASE WHEN g.white_account_id = s.user_account_id THEN 'white' ELSE 'black' END,
                            COALESCE((SELECT COALESCE(os.name, ou.display_name) FROM user_account ou
                                        LEFT JOIN student os ON os.user_account_id = ou.user_account_id
                                       WHERE ou.user_account_id = CASE WHEN g.white_account_id = s.user_account_id
                                                                       THEN g.black_account_id ELSE g.white_account_id END), ''),
                            g.timed, g.lichess_clock_limit, g.lichess_clock_increment,
                            EXISTS(SELECT 1 FROM game_challenge c WHERE c.game_room_id = g.game_room_id)
                     FROM game_room g
                     JOIN student s ON s.user_account_id IN (g.white_account_id, g.black_account_id)
                     WHERE s.student_id = ? AND g.status IN ('Finished','Active')
                     ORDER BY COALESCE(g.ended_at, g.created_at) DESC LIMIT 200`, studentID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		e := Entry{Kind: "room"}
		var timed, limit, increment int
		var fromChallenge bool
		if err := rows.Scan(&e.ID, &e.At, &e.Against, &e.Result, &e.Reason, &e.LichessGameID, &e.Moves, &e.StartedAt,
			&e.Side, &e.Opponent, &timed, &limit, &increment, &fromChallenge); err == nil {
			e.Day = academyDayOf(e.At)
			e.GameType = "class"
			if fromChallenge {
				e.GameType = "challenge"
			}
			if timed == 1 {
				e.TimeControl = clockLabel(limit, increment)
			}
			if e.Result != "" {
				e.Points = game.FinishedGamePoints
			}
			out = append(out, e)
		}
	}
	rows.Close()

	// Puzzles, which are already recorded and were simply never shown.
	rows, err = d.Query(`SELECT a.puzzle_id, COALESCE(a.solved_at, a.assigned_on),
                            p.rating, a.solved, a.wrong_moves, a.assigned_on,
                            COALESCE(a.opened_at,''), a.source
                     FROM puzzle_attempt a JOIN puzzle p ON p.puzzle_id = a.puzzle_id
                     WHERE a.student_id = ?
                     ORDER BY COALESCE(a.solved_at, a.assigned_on) DESC LIMIT 200`, studentID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var rating, solved, wrong int
		e := Entry{Kind: "puzzle"}
		// assigned_on is already the academy's day.
		if err := rows.Scan(&e.ID, &e.At, &rating, &solved, &wrong, &e.Day, &e.StartedAt, &e.Source); err == nil {
			e.Against = strconv.Itoa(rating)
			e.Moves = wrong
			if solved == 1 {
				e.Result = "solved"
				e.Points = game.SolvedPuzzlePoints
			} else {
				e.Result = "unsolved"
			}
			out = append(out, e)
		}
	}
	rows.Close()

	sortHistory(out)

	return out, nil
}

// academyDayOf is the academy's calendar day for a timestamp SQLite wrote
// with datetime('now'), which is UTC. Anything else passes through by its
// leading date.
func academyDayOf(stamp string) string {
	if t, err := time.ParseInLocation(time.DateTime, stamp, time.UTC); err == nil {
		return t.In(academytime.Now().Location()).Format(academytime.DayLayout)
	}
	if len(stamp) >= 10 {
		return stamp[:10]
	}
	return stamp
}

// sortHistory puts the whole timeline in one order, newest first. The three
// reads are merged here rather than in SQL: they have different shapes and a
// UNION would need every column padded to match.
func sortHistory(all []Entry) {
	sort.SliceStable(all, func(i, j int) bool { return all[i].At > all[j].At })
}
