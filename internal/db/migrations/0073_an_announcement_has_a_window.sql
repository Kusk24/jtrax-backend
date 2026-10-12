-- 0073_an_announcement_has_a_window.sql — an announcement can be shown to
-- parents from a time and until a time, both on the academy's clock
-- ("YYYY-MM-DDTHH:MM", Bangkok). Blank means from posting / with no end.
--
-- notified_at records that parents were told, so a scheduled one is told once
-- when its window opens. Everything already posted has been told.
ALTER TABLE announcement ADD COLUMN show_from TEXT;
ALTER TABLE announcement ADD COLUMN show_until TEXT;
ALTER TABLE announcement ADD COLUMN notified_at TEXT;
UPDATE announcement SET notified_at = COALESCE(posted_at, datetime('now'));
