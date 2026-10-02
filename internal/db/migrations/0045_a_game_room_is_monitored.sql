-- 0045_a_game_room_is_monitored.sql — what the console needs to watch a class
-- of boards at once and keep the record of who played whom.
--
-- # A time control on any game
--
-- The clock columns from 0012 carry a 15+10 default on every room, rated or
-- not, so an unrated room looked timed when nobody had chosen a clock for it.
-- `timed` says whether one was actually chosen. A rated room always is, since
-- Lichess will not run one without a clock. An unrated one shows its time
-- control as a label: the academy's own board keeps no time, so nobody loses
-- on it. Only a rated game can end on time, and Lichess decides that.
ALTER TABLE game_room ADD COLUMN timed INTEGER NOT NULL DEFAULT 0;
UPDATE game_room SET timed = 1 WHERE lichess_rated = 1;

-- # The clock a rated game is running on Lichess
--
-- The game stream reports both players' remaining time on every update. It
-- used to be parsed and dropped, so neither pupil nor coach could see a clock
-- that was deciding the game. Kept with the moment it was reported, so a board
-- can count the side to move down from there between updates.
ALTER TABLE game_room ADD COLUMN clock_white_ms INTEGER;
ALTER TABLE game_room ADD COLUMN clock_black_ms INTEGER;
ALTER TABLE game_room ADD COLUMN clock_at TEXT;

-- # A draw offer
--
-- The colour that offered, while the offer stands. The opponent accepting ends
-- the game drawn "by agreement"; declining, or either side moving, clears it.
ALTER TABLE game_room ADD COLUMN draw_offer TEXT CHECK (draw_offer IN ('White','Black'));
