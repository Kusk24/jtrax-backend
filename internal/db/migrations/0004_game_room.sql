-- 0004_game_room.sql — over-the-board games played inside the portals.
-- Not part of the ER model: like auth_session and password_reset this is
-- infrastructure the product grew, not an entity from the original diagram.
--
-- A room is minted by staff and handed out as a short code. The code is the
-- only thing standing between a stranger and a seat, so it is unique, random,
-- and cheap to rotate by cancelling the room.
--
-- Seats reference user_account rather than student so a teacher can sit down
-- against a pupil for a lesson. Who a seat belongs to is resolved to a student
-- record on read, which is what the admin history reports on.

CREATE TABLE game_room (
    game_room_id     TEXT PRIMARY KEY,
    code             TEXT NOT NULL UNIQUE,
    label            TEXT,
    created_by       TEXT NOT NULL REFERENCES user_account(user_account_id),
    -- Open: waiting for seats. Active: both seats taken, clock running.
    -- Finished: a result exists. Cancelled: staff pulled it before the end.
    status           TEXT NOT NULL DEFAULT 'Open'
                     CHECK (status IN ('Open','Active','Finished','Cancelled')),
    white_account_id TEXT REFERENCES user_account(user_account_id),
    black_account_id TEXT REFERENCES user_account(user_account_id),
    -- The authoritative position. Every accepted move rewrites it, so a
    -- reconnecting client can resume from one row without replaying moves.
    fen              TEXT NOT NULL,
    result           TEXT CHECK (result IN ('1-0','0-1','1/2-1/2')),
    result_reason    TEXT,
    created_at       TEXT NOT NULL DEFAULT (datetime('now')),
    started_at       TEXT,
    ended_at         TEXT,
    -- Two people, not three: the same account cannot hold both seats, which
    -- would otherwise let one student play themselves into a winning record.
    CHECK (white_account_id IS NULL OR white_account_id <> black_account_id)
);

CREATE INDEX idx_game_room_status ON game_room(status);
CREATE INDEX idx_game_room_white ON game_room(white_account_id);
CREATE INDEX idx_game_room_black ON game_room(black_account_id);

-- One row per half-move. The composite primary key is the concurrency control:
-- two clients racing to submit ply 7 cannot both win, because the second INSERT
-- violates the key rather than appending a second move to the same turn.
CREATE TABLE game_move (
    game_room_id TEXT NOT NULL REFERENCES game_room(game_room_id),
    ply          INTEGER NOT NULL,
    san          TEXT NOT NULL,
    uci          TEXT NOT NULL,
    fen_after    TEXT NOT NULL,
    created_at   TEXT NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (game_room_id, ply)
);
