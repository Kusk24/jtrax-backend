-- 0009_tournament_results.sql — rounds, pairings and results for a tournament.
--
-- The ER model has tournaments, categories and registrations but nothing for
-- what actually happened, which is why the console's tournament screen shows
-- every player with a score of "—" and zero wins. Those are placeholders, not
-- data. This is the missing half.
--
-- Pairings rather than a score column per player. A score column would be
-- half the size and could not answer "who did she play?", could not print a
-- cross-table, and could not compute a Buchholz tiebreak — which is what
-- decides a Swiss event when two children finish level, and therefore what
-- decides who goes home with the trophy.

-- Whether the standings may be read without signing in.
--
-- Opt-in, default off. These are children's names and results; publishing them
-- is a decision an organiser makes per event, not something that happens
-- because a table exists.
ALTER TABLE tournament ADD COLUMN results_public INTEGER NOT NULL DEFAULT 0;

CREATE TABLE tournament_round (
    tournament_round_id TEXT PRIMARY KEY,
    tournament_id       TEXT NOT NULL REFERENCES tournament(tournament_id),
    round_no            INTEGER NOT NULL,
    status              TEXT NOT NULL DEFAULT 'Pending'
                        CHECK (status IN ('Pending','Playing','Completed')),
    started_at          TEXT,
    completed_at        TEXT,
    UNIQUE (tournament_id, round_no)
);

CREATE INDEX idx_tournament_round ON tournament_round(tournament_id, round_no);

CREATE TABLE tournament_pairing (
    tournament_pairing_id TEXT PRIMARY KEY,
    tournament_round_id   TEXT NOT NULL REFERENCES tournament_round(tournament_round_id),
    board_no              INTEGER NOT NULL,
    white_registration_id TEXT NOT NULL REFERENCES tournament_registration(tournament_registration_id),
    -- Null is a bye: an odd number of players means somebody sits out, and a
    -- bye is a real row with a real point, not a missing game.
    black_registration_id TEXT REFERENCES tournament_registration(tournament_registration_id),
    -- '+/-' and '-/+' are forfeits, which score like a win but are not a game
    -- played — the distinction matters for tiebreaks and for a parent asking
    -- why their child has a point but no moves.
    result                TEXT NOT NULL DEFAULT 'Pending'
                          CHECK (result IN ('Pending','1-0','0-1','1/2-1/2','+/-','-/+','bye')),
    recorded_at           TEXT,
    recorded_by           TEXT REFERENCES user_account(user_account_id),
    UNIQUE (tournament_round_id, board_no),
    -- Nobody plays themselves, and the check is here rather than in the
    -- handler so no future caller can route around it.
    CHECK (black_registration_id IS NULL OR white_registration_id <> black_registration_id)
);

CREATE INDEX idx_pairing_round ON tournament_pairing(tournament_round_id, board_no);
CREATE INDEX idx_pairing_white ON tournament_pairing(white_registration_id);
CREATE INDEX idx_pairing_black ON tournament_pairing(black_registration_id);
