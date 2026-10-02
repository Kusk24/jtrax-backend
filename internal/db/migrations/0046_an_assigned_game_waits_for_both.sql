-- 0046_an_assigned_game_waits_for_both.sql — a game the office sets up starts
-- when both students have sat down, not when the office presses the button.
--
-- The office names White and Black, and the game appears on both students'
-- home screens. Each one presses Enter when they are at the board; the game
-- stays Waiting until both have, and only then is it in play — so nobody's
-- clock (on a rated game, Lichess's) starts while the other child is still
-- finishing a puzzle across the room.
--
-- Joining with a code counts as entering: typing the code is sitting down.
ALTER TABLE game_room ADD COLUMN white_entered_at TEXT;
ALTER TABLE game_room ADD COLUMN black_entered_at TEXT;

-- Every seat already filled was filled by someone who sat down — by code, or
-- by accepting a challenge — so those count as entered.
UPDATE game_room SET white_entered_at = COALESCE(started_at, created_at) WHERE white_account_id IS NOT NULL;
UPDATE game_room SET black_entered_at = COALESCE(started_at, created_at) WHERE black_account_id IS NOT NULL;
