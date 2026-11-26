// Student practice, settings and puzzles served by the shared CRUD API.
package api

import (
	"github.com/Kusk24/jtrax-backend/internal/chessplay"
	"github.com/Kusk24/jtrax-backend/internal/records"
)

func practiceResources() []*records.Resource {
	return []*records.Resource{
		{
			Name: "practice-activities", Table: "practice_activity", IDCol: "activity_id", IDPrefix: "act",
			Cols: []records.Col{
				{Name: "student_id", Kind: "text", Required: true},
				{Name: "activity_date", Kind: "text", Required: true},
				{Name: "minutes_practiced", Kind: "int"},
				{Name: "puzzles_completed", Kind: "int"},
				{Name: "points_earned", Kind: "int"},
				// `streak_count` is deliberately not writable: the streak is
				// derived from these dates now (see practice.go), so a stored
				// number could only disagree with the truth.
			},
			// Pupils read their own practice but no longer write it — the
			// server records a solve when it grades one. The portal used to
			// post its own row, including the streak it thought it had.
			ReadRoles: []string{"Parent", "Student"}, WriteRoles: nil,
			Scope: map[string]records.ScopeFn{
				"Parent":  byParentStudents("student_id"),
				"Student": byOwnStudent("student_id"),
			},
			Own: ownChild,
		},
		{
			Name: "practice-settings", Table: "practice_settings", IDCol: "student_id", IDPrefix: "",
			Cols: []records.Col{
				{Name: "daily_screen_time_limit_minutes", Kind: "int", Required: true},
			},
			ReadRoles: []string{"Parent", "Student"}, WriteRoles: []string{"Parent"},
			Scope: map[string]records.ScopeFn{
				"Parent":  byParentStudents("student_id"),
				"Student": byOwnStudent("student_id"),
			},
			Own: ownChild,
		},
		{
			// Authoring only. Students never touch this resource — they are
			// served by /puzzles/daily, which omits the solution. Teachers can
			// read and write so they can set the position they taught on
			// Tuesday rather than a random fork.
			Name: "puzzles", Table: "puzzle", IDCol: "puzzle_id", IDPrefix: "pzl",
			Cols: []records.Col{
				{Name: "fen", Kind: "text", Required: true},
				{Name: "moves", Kind: "text", Required: true},
				{Name: "rating", Kind: "int"},
				{Name: "themes", Kind: "text"},
				{Name: "source", Kind: "text", Enum: chessplay.PuzzleSources},
				{Name: "created_by", Kind: "text"},
			},
			ReadRoles: []string{"Teacher"}, WriteRoles: []string{"Teacher"},
			Check: chessplay.CheckPuzzle,
		},
	}
}
