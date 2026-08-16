-- 0005_puzzle.sql — tactics puzzles and the record of who solved what.
--
-- Not part of the ER model: the diagram has practice_activity, which counts
-- puzzles_completed, but nothing behind it. The portal shipped with three
-- puzzles hard-coded in the frontend, identical for every pupil, forever.
--
-- `moves` is the solution and is never sent to a pupil. Grading happens on the
-- server for the same reason move legality does: a solution the client holds is
-- a solution the client can read, and these feed streaks a parent sees.

CREATE TABLE puzzle (
    puzzle_id  TEXT PRIMARY KEY,
    -- The position the pupil is shown, with the pupil to move.
    --
    -- Lichess distributes the position *before* the opponent's move, with that
    -- move as the first entry in the solution. The importer applies it, so
    -- everything downstream can treat `fen` as "your turn" without knowing
    -- where the puzzle came from.
    fen        TEXT NOT NULL,
    -- Space-separated UCI, alternating: pupil, opponent, pupil, …
    moves      TEXT NOT NULL,
    rating     INTEGER NOT NULL DEFAULT 1000,
    themes     TEXT NOT NULL DEFAULT '',
    source     TEXT NOT NULL DEFAULT 'Lichess' CHECK (source IN ('Lichess','JCA')),
    -- Set for puzzles a teacher authored; null for imported ones.
    created_by TEXT REFERENCES user_account(user_account_id),
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);

-- The daily set is chosen by rating, so that is the index that matters.
CREATE INDEX idx_puzzle_rating ON puzzle(rating);

-- One row per pupil per puzzle per day.
--
-- Rows are written when the day's set is handed out, not when it is solved:
-- that is what makes the set stable for the rest of the day, so refreshing the
-- page cannot reroll a puzzle the pupil has just failed.
CREATE TABLE puzzle_attempt (
    puzzle_attempt_id TEXT PRIMARY KEY,
    student_id   TEXT NOT NULL REFERENCES student(student_id),
    puzzle_id    TEXT NOT NULL REFERENCES puzzle(puzzle_id),
    assigned_on  TEXT NOT NULL,
    solved       INTEGER NOT NULL DEFAULT 0,
    -- Wrong guesses. A puzzle solved first time is worth more than one brute
    -- forced, and the difference is what tells a teacher who is struggling.
    wrong_moves  INTEGER NOT NULL DEFAULT 0,
    solved_at    TEXT,
    UNIQUE (student_id, puzzle_id, assigned_on)
);

CREATE INDEX idx_puzzle_attempt_student_day ON puzzle_attempt(student_id, assigned_on);
