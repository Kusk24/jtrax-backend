-- A pupil's games against the computer, which were never recorded at all.
--
-- Deliberately, once: "losing to the computer in private is the point — it is
-- practice, not a result the academy reports on." That still holds for how it
-- is *shown*; it stopped holding for whether it is kept, because a child's
-- chess at the academy should survive the afternoon they played it.
--
-- Separate from game_room: a room has a join code, two accounts and possibly a
-- Lichess relay, none of which a solo game has. Forcing it through that table
-- would mean a NOT NULL code nobody types and a NULL opponent everywhere.
CREATE TABLE solo_game (
    solo_game_id  TEXT PRIMARY KEY,
    student_id    TEXT NOT NULL REFERENCES student(student_id),
    -- Which of the three trained opponents, so progress against each is
    -- readable rather than averaged into one number.
    opponent      TEXT NOT NULL,
    -- The pupil's colour. They are White today, but recording it means a game
    -- can be replayed correctly if that ever changes.
    student_side  TEXT NOT NULL DEFAULT 'white' CHECK (student_side IN ('white','black')),
    moves         TEXT NOT NULL DEFAULT '',
    move_count    INTEGER NOT NULL DEFAULT 0,
    result        TEXT CHECK (result IN ('1-0','0-1','1/2-1/2')),
    result_reason TEXT,
    started_at    TEXT,
    ended_at      TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX idx_solo_game_student ON solo_game(student_id, ended_at);
