-- 0029_early_bird_window_and_regulation_file.sql — what a tournament offers
-- before it starts.
--
-- The schema has carried early_bird_fee since 0001 with nothing to say when
-- early bird *ends*, so no screen could ever charge it — the fee needs its
-- window. And the regulation document was a URL column pointing at nothing:
-- organisers hand the academy a PDF, not a link, so the file itself needs a
-- home the public page can serve it from.
--
-- The file lives in the database rather than on disk because the API host's
-- disk is ephemeral — a redeploy would eat every regulation. One row per
-- tournament, capped at upload time (5 MB), and in its own table so listing
-- tournaments never drags megabytes of PDF through a SELECT.

ALTER TABLE tournament ADD COLUMN early_bird_deadline TEXT;

CREATE TABLE tournament_regulation (
    tournament_id TEXT PRIMARY KEY REFERENCES tournament(tournament_id),
    filename      TEXT NOT NULL,
    content_type  TEXT NOT NULL,
    bytes         BLOB NOT NULL,
    uploaded_at   TEXT NOT NULL DEFAULT (datetime('now'))
);
