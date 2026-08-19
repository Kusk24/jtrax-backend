-- 0012_lichess_relay.sql — a game room that is also a real, rated game on
-- lichess.org.
--
-- The board stays here. Every move a student makes in JTrax is relayed to
-- Lichess with that student's own token, so when the game ends it ends on both
-- and the rating moves for real. This is the only way a game played on our
-- board can produce a result on Lichess: imported PGNs are never rated, and
-- there is no endpoint that invents a game after the fact.
--
-- # Why a clock had to appear
--
-- Lichess will not run a rated real-time game without one, and it enforces it:
-- a pupil who wanders off loses on time *there* while our board still shows a
-- live position. So the clock is not decoration — it is the thing that stops
-- the two boards disagreeing about whether a game is still going. The values
-- are stored per room and the running remainder comes from the game stream.
--
-- # Detaching
--
-- If Lichess and JTrax ever disagree about a move, the game stops being rated
-- and says so, rather than the server pretending or silently dropping moves.
-- A game that is quietly not counting is worse than one that admits it is not.

ALTER TABLE game_room ADD COLUMN lichess_rated INTEGER NOT NULL DEFAULT 0;
-- Lichess's own id for the game, which doubles as the challenge id. NULL while
-- the room is still waiting for its second player.
ALTER TABLE game_room ADD COLUMN lichess_game_id TEXT;
-- Last status seen on the game stream: created, started, mate, resign,
-- outoftime and so on. Kept so a finished room can say *how* Lichess ended it,
-- which is not always how our own board would have.
ALTER TABLE game_room ADD COLUMN lichess_status TEXT;
-- Why the relay stopped, when it did. Shown to the players: a game that has
-- stopped counting must say so while they are still playing it, not afterwards.
ALTER TABLE game_room ADD COLUMN lichess_detached_reason TEXT;
-- Initial clock and increment in seconds. Lichess only accepts 0, 15, 30, 45,
-- 60, 90 and multiples of 60 for the limit.
ALTER TABLE game_room ADD COLUMN lichess_clock_limit INTEGER NOT NULL DEFAULT 900;
ALTER TABLE game_room ADD COLUMN lichess_clock_increment INTEGER NOT NULL DEFAULT 10;

CREATE INDEX idx_game_room_lichess ON game_room(lichess_game_id);
