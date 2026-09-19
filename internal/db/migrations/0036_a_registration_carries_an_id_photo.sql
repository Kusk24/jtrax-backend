-- 0036_a_registration_carries_an_id_photo.sql — a public tournament
-- registration now carries the applicant's own identification: a photo of
-- their Thai national ID card or passport, for the organiser to check the
-- player at the board is who they registered as.
--
-- Same shape as tournament_regulation (0029), and the same reason: the file
-- is small (a phone photo of a card, capped well under what a database row
-- minds) and this is one registration's worth of paperwork, not a document
-- library. The difference is who may ever read it back. A regulation is
-- public on purpose — a parent reads what they are signing up for before
-- they submit. A stranger's national ID or passport photo is not that: it
-- exists so staff can check a face at the check-in desk, and for nobody
-- else. There is no public GET for this table anywhere in the API, and there
-- must never be one.

CREATE TABLE tournament_registration_document (
    tournament_registration_id TEXT PRIMARY KEY
                                REFERENCES tournament_registration(tournament_registration_id),
    filename                    TEXT NOT NULL,
    content_type                TEXT NOT NULL,
    bytes                       BLOB NOT NULL,
    uploaded_at                 TEXT NOT NULL DEFAULT (datetime('now'))
);
