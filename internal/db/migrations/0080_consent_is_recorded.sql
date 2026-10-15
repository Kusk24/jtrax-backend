-- 0080_consent_is_recorded.sql — what a family agreed to, and when.
--
-- PDPA asks a controller to show that a privacy notice was given (s.23) and,
-- for sensitive data such as health, that explicit consent was (s.26). Until
-- now the only record was tournament_registration.terms_accepted_at, which
-- covers the entry conditions and nothing about personal data. One row per
-- agreement: which notice version it was, who gave it (an account, or the
-- name typed on the public form), and when — stamped by the server.
-- Deleting an entry deletes what was agreed for it; deleting an account
-- keeps its agreements, by name, as the evidence they are.
CREATE TABLE consent_record (
    consent_id                  TEXT PRIMARY KEY,
    purpose                     TEXT NOT NULL CHECK (purpose IN ('privacy_notice', 'health_data')),
    notice_version              TEXT NOT NULL,
    tournament_registration_id  TEXT REFERENCES tournament_registration(tournament_registration_id) ON DELETE CASCADE,
    user_account_id             TEXT REFERENCES user_account(user_account_id) ON DELETE SET NULL,
    given_by_name               TEXT NOT NULL DEFAULT '',
    given_at                    TEXT NOT NULL DEFAULT (datetime('now')),
    withdrawn_at                TEXT
);
CREATE INDEX idx_consent_registration ON consent_record(tournament_registration_id);
CREATE INDEX idx_consent_account ON consent_record(user_account_id);
