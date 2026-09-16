-- Free Play and the daily challenge both record a puzzle_attempt, and the daily
-- endpoint reads every attempt dated today. Without a way to tell them apart, a
-- child who tried one Free Play puzzle found it listed as a fourth "daily"
-- puzzle, and the set stopped being three.
--
-- The bank stays shared and the never-repeat rule stays global: this only says
-- which question the row was an answer to.
ALTER TABLE puzzle_attempt ADD COLUMN source TEXT NOT NULL DEFAULT 'daily'
    CHECK (source IN ('daily', 'free'));

-- Every row that exists predates Free Play, so the default is already right for
-- them; the index is for the daily read, which is the hot path.
CREATE INDEX idx_puzzle_attempt_student_day_source
    ON puzzle_attempt(student_id, assigned_on, source);
