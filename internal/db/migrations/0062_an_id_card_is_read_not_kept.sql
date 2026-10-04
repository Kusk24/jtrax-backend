-- 0062_an_id_card_is_read_not_kept.sql — an ID card is read for the
-- player's date of birth, and the photo is not kept.
--
-- 0036 stored the photo of every public entrant's ID card or passport. The
-- card is now read once, by the scan, and only what was read is kept: the
-- date of birth that decides the age group, the name, and which document it
-- was. Those wait here, against the tournament (and the child, from the
-- parent portal), until the entry that uses them is made. The photos already
-- stored are deleted with their table.

CREATE TABLE id_card_check (
    id_card_check_id TEXT PRIMARY KEY,
    tournament_id    TEXT NOT NULL REFERENCES tournament(tournament_id),
    -- Set when a parent scanned for one of their children; NULL from the
    -- public form, which has no account behind it.
    student_id       TEXT REFERENCES student(student_id),
    date_of_birth    TEXT NOT NULL,
    name             TEXT,
    document_type    TEXT,
    created_at       TEXT NOT NULL DEFAULT (datetime('now'))
);

DROP TABLE tournament_registration_document;
