-- 0003_password_reset.sql — single-use tokens for the forgot-password flow.
-- Infrastructure like auth_session, so it is not part of the ER model.
--
-- The primary key is a SHA-256 of the token, never the token itself: the value
-- mailed to the user is the only copy, so a leak of this table cannot be
-- replayed to take over an account. used_at makes a token single-use, and
-- rows are kept after use so a replay is distinguishable from an unknown token.

CREATE TABLE password_reset (
    token_hash      TEXT PRIMARY KEY,
    user_account_id TEXT NOT NULL REFERENCES user_account(user_account_id),
    created_at      TEXT NOT NULL DEFAULT (datetime('now')),
    expires_at      TEXT NOT NULL,
    used_at         TEXT
);
CREATE INDEX idx_password_reset_user ON password_reset(user_account_id);
