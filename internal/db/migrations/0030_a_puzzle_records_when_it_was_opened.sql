-- Practice time, measured rather than guessed.
--
-- `minutes_practiced` was posted by the browser as a flat 10 every time the
-- daily set was finished, which is a number no one counted shown to a parent.
-- Removing it left 0, which is honest and useless. Timing needs a start, and
-- the attempt row only knew when a puzzle was solved.
--
-- opened_at is stamped by the server when a pupil opens the puzzle, so the
-- elapsed time is between two clocks the server owns.
ALTER TABLE puzzle_attempt ADD COLUMN opened_at TEXT;
