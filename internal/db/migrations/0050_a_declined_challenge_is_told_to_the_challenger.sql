-- 0050_a_declined_challenge_is_told_to_the_challenger.sql — a "no" is an answer.
--
-- A declined challenge used to drop off both players' lists at once, so the
-- child who sent it watched "waiting for them" simply vanish and could not tell
-- a no from a glitch. It now stays on the challenger's list, marked Declined,
-- until they dismiss it; `decline_seen_at` is when they did.
ALTER TABLE game_challenge ADD COLUMN decline_seen_at TEXT;
