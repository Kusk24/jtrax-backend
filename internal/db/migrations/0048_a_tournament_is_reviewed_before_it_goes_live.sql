-- 0048_a_tournament_is_reviewed_before_it_goes_live.sql — a draft, a private
-- preview of it, and the banner at the top of its page.
--
-- The create wizard used to write the tournament at its last step, so the
-- first time anybody saw the public page was after parents could. Now the
-- wizard saves a draft first and shows the organiser the real registration
-- page for it, through a link only the console holds (preview_token). Publish
-- is the one action that takes it live.
--
-- A draft is invisible to everyone but staff: parents, students and teachers
-- never list it, and the public endpoints already need public_registration.
--
-- The banner is optional. With none uploaded, the pages draw one from the
-- tournament's own name, date and venue. Stored like the regulation, and for
-- the same reason: the API host's disk does not survive a redeploy.

ALTER TABLE tournament ADD COLUMN draft INTEGER NOT NULL DEFAULT 0;

ALTER TABLE tournament ADD COLUMN preview_token TEXT;

CREATE TABLE tournament_banner (
    tournament_id TEXT PRIMARY KEY REFERENCES tournament(tournament_id),
    content_type  TEXT NOT NULL,
    bytes         BLOB NOT NULL,
    uploaded_at   TEXT NOT NULL DEFAULT (datetime('now'))
);
