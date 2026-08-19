-- 0013_chess_results.sql — tournaments the academy's students play in that are
-- run by *other people*, tracked from chess-results.com.
--
-- chess-results.com is the publishing side of Swiss-Manager, the program most
-- arbiters in Thailand run a tournament with. There is no API to write to it —
-- not for us, not for anyone; the arbiter uploads from their desktop. What it
-- does offer is stable, readable pages per tournament. So this is a one-way
-- read: staff paste a tournament link, the server pulls the standings, and a
-- refresh pulls them again as rounds land.
--
-- Rows are stored, not proxied live, for two reasons: the site is slow and
-- sometimes down, and every read from us should not become a read from them.
-- The page a parent sees is served from here and says when it was fetched.

-- FIDE ID is the join key between our students and anyone else's tournament
-- table. It identifies a player for life across every rated event, which a
-- name never can — the same child appears as "Somchai, N.", "Somchai, Niran"
-- and a Thai-script spelling in the same season.
ALTER TABLE student ADD COLUMN fide_id TEXT;

CREATE TABLE external_tournament (
    external_tournament_id TEXT PRIMARY KEY,
    -- The number in tnr{N}.aspx — chess-results' own identity for the event.
    chess_results_id       INTEGER NOT NULL UNIQUE,
    name                   TEXT NOT NULL,
    -- What the site's own heading said of the table: "Final Ranking after 9
    -- Rounds", "Rank after Round 4", or empty for an event not yet started.
    -- Stored verbatim because it is the honest description of how final the
    -- numbers are, and inventing our own would be a translation risk.
    stage                  TEXT NOT NULL DEFAULT '',
    fetched_at             TEXT,
    created_at             TEXT NOT NULL DEFAULT (datetime('now')),
    created_by             TEXT REFERENCES user_account(user_account_id)
);

CREATE TABLE external_standing (
    external_tournament_id TEXT NOT NULL REFERENCES external_tournament(external_tournament_id),
    -- Row order in the source table. This, not rank, is the identity: the site
    -- blanks the rank on shared and unranked rows, so displayed ranks repeat.
    position               INTEGER NOT NULL,
    rank                   INTEGER NOT NULL,
    name                   TEXT NOT NULL,
    fide_id                TEXT NOT NULL DEFAULT '',
    federation             TEXT NOT NULL DEFAULT '',
    rating                 INTEGER NOT NULL DEFAULT 0,
    points                 REAL NOT NULL DEFAULT 0,
    club                   TEXT NOT NULL DEFAULT '',
    -- Filled when a row is recognised as one of ours: by FIDE ID when both
    -- sides have one, else by exact name. NULL is the normal case — most rows
    -- in an open tournament are other academies' children.
    student_id             TEXT REFERENCES student(student_id),
    PRIMARY KEY (external_tournament_id, position)
);

CREATE INDEX idx_external_standing_student ON external_standing(student_id);
