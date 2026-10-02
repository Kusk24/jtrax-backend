-- Practice becomes one list of twenty puzzles per pupil, levels mixed, in
-- place of ten per level (0057).
--
-- Two in three come from the pupil's own level; the rest are split between
-- the other two, the nearer level taking more. A solved puzzle is ticked and
-- can be played again that day; the next day it is replaced by a new one,
-- while the unsolved ones stay. First solves are written to puzzle_attempt,
-- so nothing is lost when a row here is replaced.
DROP TABLE IF EXISTS practice_puzzle;

CREATE TABLE puzzle_list (
    student_id  TEXT NOT NULL REFERENCES student(student_id),
    puzzle_id   TEXT NOT NULL REFERENCES puzzle(puzzle_id),
    position    INTEGER NOT NULL,
    tier        TEXT NOT NULL CHECK (tier IN ('beginner','intermediate','advanced')),
    opened_at   TEXT,
    -- The academy day it was first solved, YYYY-MM-DD. Replaced once a later
    -- day asks for the list.
    solved_on   TEXT,
    added_on    TEXT NOT NULL,
    PRIMARY KEY (student_id, puzzle_id)
);

CREATE INDEX idx_puzzle_list_student ON puzzle_list(student_id);
