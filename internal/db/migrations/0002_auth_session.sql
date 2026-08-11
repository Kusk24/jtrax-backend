-- 0002_auth_session.sql — server-side session store for opaque bearer tokens.
-- Not part of the ER model (pure infrastructure), so it lives in its own migration.

CREATE TABLE auth_session (
    token           TEXT PRIMARY KEY,
    user_account_id TEXT NOT NULL REFERENCES user_account(user_account_id),
    created_at      TEXT NOT NULL DEFAULT (datetime('now')),
    expires_at      TEXT NOT NULL
);
CREATE INDEX idx_auth_session_user ON auth_session(user_account_id);
