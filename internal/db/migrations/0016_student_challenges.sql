-- 0016_student_challenges.sql — one student inviting another to a game.
--
-- Until now a board could only be minted by staff and handed out as a code, so
-- two pupils who wanted a game had to ask an adult for one. A challenge is the
-- pupil-initiated version: A names B, B says yes, and a room appears already
-- seated for both.
--
-- It is a separate table rather than a room in a "Pending" state, because the
-- two things have different lifetimes and different rules. A challenge can be
-- declined, expires as a social fact rather than a game one, and must not
-- occupy a code or appear in anybody's game history. A room that was never
-- accepted is not a game that was never played — it is a conversation that went
-- nowhere, and the schema should not confuse the two.
--
-- Seats reference user_account, matching game_room: the same challenge shape
-- works for a teacher accepting a pupil's invitation to a lesson game.

CREATE TABLE game_challenge (
    game_challenge_id TEXT PRIMARY KEY,
    from_account_id   TEXT NOT NULL REFERENCES user_account(user_account_id),
    to_account_id     TEXT NOT NULL REFERENCES user_account(user_account_id),

    status            TEXT NOT NULL DEFAULT 'Pending'
                      CHECK (status IN ('Pending','Accepted','Declined','Cancelled')),

    -- What was asked for, kept on the challenge rather than only on the room:
    -- the room does not exist until somebody accepts, and the person deciding
    -- needs to see what they are agreeing to.
    rated             INTEGER NOT NULL DEFAULT 0,
    clock_limit       INTEGER NOT NULL DEFAULT 900,
    clock_increment   INTEGER NOT NULL DEFAULT 10,

    -- Set when accepted. This is the only link between the invitation and the
    -- game it produced.
    game_room_id      TEXT REFERENCES game_room(game_room_id),

    created_at        TEXT NOT NULL DEFAULT (datetime('now')),
    responded_at      TEXT,

    -- Nobody challenges themselves; without this a pupil could seat both sides
    -- and manufacture a winning record, which is the same hole game_room's own
    -- CHECK closes one step later.
    CHECK (from_account_id <> to_account_id)
);

-- One live invitation per direction. Re-sending should be a no-op rather than a
-- second row, and without this a pupil could fill somebody's inbox by holding
-- down a button. Declined and cancelled rows are excluded so the same pair can
-- try again later — a decline is "not now", not "never".
CREATE UNIQUE INDEX idx_challenge_one_pending
    ON game_challenge(from_account_id, to_account_id)
    WHERE status = 'Pending';

-- The inbox query: "what is waiting for me".
CREATE INDEX idx_challenge_inbox ON game_challenge(to_account_id, status);
CREATE INDEX idx_challenge_outbox ON game_challenge(from_account_id, status);
