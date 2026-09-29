-- 0041 — a session row holds the SHA-256 of its bearer token, not the token.
--
-- Until now auth_session.token was the bearer token itself, so a copy of the
-- database file was every signed-in account for up to thirty days. Password
-- reset links and public pay codes were already stored hashed; sessions were
-- the one credential kept in clear. The column is renamed so no query can go
-- on comparing a raw token against it by mistake.
--
-- The existing rows cannot be converted here — SQLite has no SHA-256 — and a
-- raw token left under the new name would match nothing anyway, so they are
-- removed. Everybody signs in once more after this runs.
DELETE FROM auth_session;
ALTER TABLE auth_session RENAME COLUMN token TO token_hash;
