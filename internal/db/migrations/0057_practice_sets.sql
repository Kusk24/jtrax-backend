-- Practice by level: a pupil opens Beginner, Intermediate or Advanced and is
-- shown a set of ten puzzles to pick from, rather than one at a time.
--
-- A set is kept, so the list is the same when they come back; a solved puzzle
-- stays in it with a tick and can be played again. Only the first solve counts
-- towards points and the practice record — that is written to puzzle_attempt
-- as a free-play solve, so progress, history and the dashboard read it without
-- a special case. When every puzzle in a set is solved the pupil can ask for
-- the next ten (set_no + 1).
CREATE TABLE practice_puzzle (
    student_id  TEXT NOT NULL REFERENCES student(student_id),
    tier        TEXT NOT NULL CHECK (tier IN ('beginner','intermediate','advanced')),
    set_no      INTEGER NOT NULL DEFAULT 1,
    position    INTEGER NOT NULL,
    puzzle_id   TEXT NOT NULL REFERENCES puzzle(puzzle_id),
    opened_at   TEXT,
    solved_at   TEXT,
    created_at  TEXT NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (student_id, puzzle_id)
);

CREATE INDEX idx_practice_puzzle_set ON practice_puzzle(student_id, tier, set_no);
