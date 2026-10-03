-- A tournament asks its entrants whether they are coming.
--
-- The organiser sets how many days before the start JTrax emails every
-- approved entrant a link to confirm or decline. NULL (or 0) sends nothing.
-- Each entry keeps the answer, when it was asked, and the hash of the code in
-- its link — the code itself is only ever in the email.
ALTER TABLE tournament ADD COLUMN arrival_reminder_days INTEGER
    CHECK (arrival_reminder_days IS NULL OR arrival_reminder_days BETWEEN 0 AND 365);

ALTER TABLE tournament_registration ADD COLUMN arrival_status TEXT NOT NULL DEFAULT 'Pending'
    CHECK (arrival_status IN ('Pending','Confirmed','NotAttending'));
ALTER TABLE tournament_registration ADD COLUMN arrival_reminded_at TEXT;
ALTER TABLE tournament_registration ADD COLUMN arrival_answered_at TEXT;
ALTER TABLE tournament_registration ADD COLUMN arrival_code_hash TEXT;
