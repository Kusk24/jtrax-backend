-- 0008_lichess.sql — a student's Lichess account, and the ratings synced from it.
--
-- Lichess is where students play at home. The academy already sees everything
-- played *here* — game_room records every move — and nothing played there, so
-- this closes the half of a student's practice the school is blind to.
--
-- Numbered 0008 so 0007 stays free for the LINE migration on its own branch.
-- The runner applies whatever files exist in filename order, so a gap is
-- harmless if the branches land out of order.

CREATE TABLE student_lichess (
    student_id   TEXT PRIMARY KEY REFERENCES student(student_id),
    -- As the student typed it, for display: Lichess preserves the case a
    -- username was registered with even though it matches case-insensitively.
    username     TEXT NOT NULL,
    -- The canonical lowercase id the API returns. Every lookup uses this, so a
    -- student typing "Penny" and "penny" cannot produce two links.
    lichess_id   TEXT NOT NULL UNIQUE,

    -- Whether the student proved the account is theirs.
    --
    -- This matters more than it looks. A typed username is a claim: nothing
    -- stops a pupil entering a grandmaster's account and appearing at 3000 on
    -- a screen their parents see. Unverified links are kept — a teacher
    -- recording a known username is useful — but they are marked, and the
    -- console shows the difference rather than presenting both as fact.
    verified     INTEGER NOT NULL DEFAULT 0,
    -- The one-time string the student pastes into their Lichess bio. Cleared
    -- once used, so a code cannot be replayed for a different account.
    verify_code  TEXT,
    linked_at    TEXT NOT NULL DEFAULT (datetime('now')),
    -- Who created the link: the student themselves, or a member of staff.
    linked_by    TEXT REFERENCES user_account(user_account_id),
    -- When Lichess was last read for this student, successfully or not.
    synced_at    TEXT
);

-- Current ratings, one row per game type ("perf" in Lichess's vocabulary:
-- blitz, rapid, classical, puzzle, …).
--
-- A row per perf rather than a column per perf, because Lichess has a dozen and
-- adds variants; a wide table would need a migration every time the academy
-- decided it cared about chess960.
CREATE TABLE lichess_rating (
    student_id  TEXT NOT NULL REFERENCES student(student_id),
    perf        TEXT NOT NULL,
    rating      INTEGER NOT NULL,
    games       INTEGER NOT NULL DEFAULT 0,
    -- Lichess marks a rating provisional until enough games have been played.
    -- Those numbers swing wildly and must not be presented as an achievement,
    -- so the flag is carried through rather than dropped on import.
    provisional INTEGER NOT NULL DEFAULT 0,
    synced_at   TEXT NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (student_id, perf)
);

-- One rating per student per game type per day, so improvement over a term is
-- visible rather than just today's number.
--
-- Written by the same sync with INSERT OR IGNORE on the day: syncing four times
-- in an afternoon leaves one row, and the first reading of the day is the one
-- kept.
CREATE TABLE lichess_rating_day (
    student_id  TEXT NOT NULL REFERENCES student(student_id),
    perf        TEXT NOT NULL,
    on_date     TEXT NOT NULL,
    rating      INTEGER NOT NULL,
    games       INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (student_id, perf, on_date)
);

CREATE INDEX idx_lichess_day_student ON lichess_rating_day(student_id, perf, on_date);
