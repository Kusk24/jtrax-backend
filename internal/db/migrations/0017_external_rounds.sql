-- 0017_external_rounds.sql — per-round pairings for tournaments tracked from
-- chess-results.com.
--
-- The academy runs its events in Swiss-Manager and publishes to
-- chess-results.com after every round: pairings go up before play, results
-- after. The standings table (0013) only ever showed the ranking; this stores
-- the round pages too, because "which board is my child on" is the question a
-- parent at the venue actually has, and the ranking never answers it.
--
-- Rows are the arbiter's, verbatim — names as spelled there, results as
-- printed ("1 - 0", "1" on a bye, empty before the game). A round the ranking
-- has counted is finished and never refetched; the round after it is refetched
-- while live, because its pairings can be corrected until play starts.

CREATE TABLE external_round (
    external_tournament_id TEXT NOT NULL REFERENCES external_tournament(external_tournament_id),
    round_no               INTEGER NOT NULL,
    -- 1 once the ranking heading has counted this round ("Rank after Round N"
    -- covers rounds 1..N). A played round is immutable; an unplayed one is
    -- replaced wholesale on every refresh.
    played                 INTEGER NOT NULL DEFAULT 0,
    -- The date as the site prints it, informational only.
    round_date             TEXT NOT NULL DEFAULT '',
    fetched_at             TEXT,
    PRIMARY KEY (external_tournament_id, round_no)
);

CREATE TABLE external_pairing (
    external_tournament_id TEXT NOT NULL,
    round_no               INTEGER NOT NULL,
    board                  INTEGER NOT NULL,
    white_name             TEXT NOT NULL DEFAULT '',
    white_rating           INTEGER NOT NULL DEFAULT 0,
    -- "bye" and "not paired" keep the site's own wording in black_name.
    black_name             TEXT NOT NULL DEFAULT '',
    black_rating           INTEGER NOT NULL DEFAULT 0,
    result                 TEXT NOT NULL DEFAULT '',
    -- Filled when a seat is recognised as one of ours, same rule as
    -- external_standing: FIDE ID when possible, exact normalised name else.
    white_student_id       TEXT REFERENCES student(student_id),
    black_student_id       TEXT REFERENCES student(student_id),
    PRIMARY KEY (external_tournament_id, round_no, board),
    FOREIGN KEY (external_tournament_id, round_no)
        REFERENCES external_round(external_tournament_id, round_no)
);

CREATE INDEX idx_external_pairing_students
    ON external_pairing(white_student_id, black_student_id);
