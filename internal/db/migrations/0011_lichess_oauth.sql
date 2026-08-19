-- 0011_lichess_oauth.sql — a student's Lichess access token, so the academy can
-- play *on* Lichess on their behalf rather than only read their ratings.
--
-- 0008 could read a public profile with no credential at all. Playing a rated
-- game cannot: it needs a token the student themselves granted. That token is
-- the most dangerous thing this database holds — it can resign the student's
-- games and accept challenges as them — so it is sealed with the same
-- AES-256-GCM box as the LINE channel secret and never leaves the server.
--
-- # Why this replaces the bio code rather than joining it
--
-- 0008 proved ownership by having the student paste a code into their public
-- bio. An OAuth grant proves the same thing far better: it is issued by Lichess
-- to the account holder after they authenticate. A student who completes the
-- grant is verified, full stop, and never sees a code. The bio path stays for
-- students who want ratings tracked without granting play access at all.
--
-- # Expiry
--
-- Lichess issues tokens with expires_in of one year and **no refresh token**.
-- There is no renewal loop to write; there is an expiry date to watch and a
-- student to ask again once a year. Storing the date is what lets the portal
-- warn before a token dies mid-tournament instead of after.

ALTER TABLE student_lichess ADD COLUMN token_enc TEXT;
ALTER TABLE student_lichess ADD COLUMN token_expires_at TEXT;
-- Recorded because scopes granted are not always scopes asked for: Lichess lets
-- the account holder untick permissions on the consent screen. A link that came
-- back without board:play can read ratings but must not be offered a rated
-- game, and the difference has to be visible rather than discovered at the
-- board.
ALTER TABLE student_lichess ADD COLUMN token_scopes TEXT;
ALTER TABLE student_lichess ADD COLUMN authorized_at TEXT;
-- Who holds the Lichess account's password. Lichess requires an account holder
-- to be 13, so a younger pupil plays on an account their parent or teacher
-- created and keeps in Kid Mode. That is a real safeguarding fact about a
-- child, not a UI detail, so it is recorded rather than inferred from age.
ALTER TABLE student_lichess ADD COLUMN managed_by TEXT REFERENCES user_account(user_account_id);

-- One row per authorization attempt in flight.
--
-- PKCE requires the verifier that produced the challenge to be presented at the
-- token exchange, and the exchange happens in a *different request* to the one
-- that started the flow — the student's browser goes to Lichess in between. So
-- the verifier has to outlive the first request, and this is where it waits.
--
-- The state doubles as the callback's authentication. That endpoint cannot
-- carry a session: the browser arrives on a redirect from lichess.org with no
-- Authorization header of ours. What makes it safe is that state is random,
-- single-use, short-lived and already bound to a student here — so a caller
-- cannot aim a stolen code at somebody else's account.
CREATE TABLE lichess_oauth_state (
    state         TEXT PRIMARY KEY,
    code_verifier TEXT NOT NULL,
    student_id    TEXT NOT NULL REFERENCES student(student_id),
    -- The account that began the flow. Usually the student; for a young pupil
    -- it is the parent or teacher doing it with them, and that is who becomes
    -- managed_by on success.
    started_by    TEXT NOT NULL REFERENCES user_account(user_account_id),
    -- Where to send the browser once the exchange succeeds or fails. Validated
    -- against the configured portal origins before use — an open redirect here
    -- would turn the academy's domain into a phishing hop.
    return_to     TEXT,
    created_at    TEXT NOT NULL DEFAULT (datetime('now')),
    -- Consumed rather than deleted, so a replayed code meets a row that says
    -- "already used" instead of one that says "never existed". The two are
    -- different bugs and the logs should be able to tell them apart.
    used_at       TEXT
);

CREATE INDEX idx_lichess_oauth_state_created ON lichess_oauth_state(created_at);
