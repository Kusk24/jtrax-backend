-- 0014_public_registration.sql — let anyone register for a tournament, not
-- only the academy's own students.
--
-- Until now a registration *was* a student: `student_id` was NOT NULL, so the
-- table could not express the ordinary case of an open event, which is a child
-- from another school whose name the academy has never seen. Opening
-- registration to the public therefore starts here rather than at an endpoint.
--
-- Three things change:
--
--   * `student_id` becomes nullable — NULL means "a member of the public", and
--     the name and contact columns are then the only record of who they are;
--   * a registration gains a *status*, because a stranger's submission must not
--     become a participant the instant it arrives. Public sign-ups land as
--     Pending and a member of staff approves them;
--   * the tournament gains the two things an open event needs and did not have:
--     a switch that opens registration, and the discount the academy's own
--     students get.
--
-- Existing rows are staff-entered and already real participants, so they are
-- migrated as Approved/Staff — the console behaves exactly as it did before.

-- Opt-in, exactly like results_public: a tournament is closed until an
-- organiser says otherwise.
ALTER TABLE tournament ADD COLUMN public_registration INTEGER NOT NULL DEFAULT 0;

-- Percent off the fee for a registrant recognised as one of the academy's
-- students. A percentage rather than a second fee column because that is how
-- the front desk says it ("twenty percent off for our kids"), and because it
-- stays correct when the fee is edited afterwards.
ALTER TABLE tournament ADD COLUMN student_discount_pct INTEGER NOT NULL DEFAULT 0
    CHECK (student_discount_pct BETWEEN 0 AND 100);

-- SQLite cannot drop NOT NULL in place, so the table is rebuilt. The wrinkle is
-- the same one 0006 hit: tournament_pairing points into the table being
-- dropped, and DROP TABLE raises one immediate foreign-key violation per
-- referencing row. `white_registration_id` is NOT NULL so the references
-- cannot simply be nulled out, as they were there. Instead the pairings are
-- parked wholesale, deleted, and put back once the new table exists — at no
-- point is a reference dangling, so no pragma is needed.
CREATE TABLE pairing_backup AS SELECT * FROM tournament_pairing;

DELETE FROM tournament_pairing;

CREATE TABLE tournament_registration_new (
    tournament_registration_id TEXT PRIMARY KEY,
    tournament_id              TEXT NOT NULL REFERENCES tournament(tournament_id),
    -- NULL for a member of the public. Set when the registrant is one of ours,
    -- either because staff entered them or because a public sign-up was
    -- recognised by the email address the academy already holds for them.
    student_id                 TEXT REFERENCES student(student_id),
    participant_name           TEXT NOT NULL,
    participant_contact        TEXT,
    participant_date_of_birth  TEXT,
    tournament_category_id     TEXT REFERENCES tournament_category(tournament_category_id),
    fide_rating                REAL,
    fee_charged                REAL,
    registered_at              TEXT NOT NULL DEFAULT (datetime('now')),

    -- Pending is the public front door; Approved is what staff entry has always
    -- meant. Rejected rows are kept rather than deleted so the same person
    -- cannot quietly resubmit past a decision, and so the desk can see what it
    -- turned away.
    status                     TEXT NOT NULL DEFAULT 'Approved'
                               CHECK (status IN ('Pending','Approved','Rejected','Withdrawn')),
    source                     TEXT NOT NULL DEFAULT 'Staff'
                               CHECK (source IN ('Staff','Public')),

    -- Kept apart from participant_contact, which is free text staff have used
    -- for a phone number, a parent's name or nothing at all. These two are
    -- structured because the public form validates them and the desk replies to
    -- them.
    contact_email              TEXT NOT NULL DEFAULT '',
    contact_phone              TEXT NOT NULL DEFAULT '',

    -- What the registrant was actually quoted, and whether the student discount
    -- was part of it. Stored rather than recomputed: the fee is a promise made
    -- at a moment, and editing the tournament's price later must not silently
    -- rewrite what somebody was told they owed.
    fee_quoted                 REAL,
    student_discount_applied   INTEGER NOT NULL DEFAULT 0,

    reviewed_at                TEXT,
    reviewed_by                TEXT REFERENCES user_account(user_account_id)
);

INSERT INTO tournament_registration_new (
    tournament_registration_id, tournament_id, student_id, participant_name,
    participant_contact, participant_date_of_birth, tournament_category_id,
    fide_rating, fee_charged, registered_at, status, source, fee_quoted
)
SELECT
    tournament_registration_id, tournament_id, student_id, participant_name,
    participant_contact, participant_date_of_birth, tournament_category_id,
    fide_rating, fee_charged, registered_at, 'Approved', 'Staff', fee_charged
FROM tournament_registration;

DROP TABLE tournament_registration;

ALTER TABLE tournament_registration_new RENAME TO tournament_registration;

INSERT INTO tournament_pairing SELECT * FROM pairing_backup;

DROP TABLE pairing_backup;

-- The old table-level UNIQUE (tournament_id, student_id) is replaced by a
-- partial index, for two reasons. It has to skip NULL student_ids anyway, and
-- it must not apply to Rejected rows: those are kept deliberately, and a
-- blanket constraint would mean one rejection bars a child from the event
-- forever, even after the misunderstanding is sorted out.
CREATE UNIQUE INDEX idx_registration_one_per_student
    ON tournament_registration(tournament_id, student_id)
    WHERE student_id IS NOT NULL AND status <> 'Rejected';

-- The same guard for people the academy has no student record for. This is what
-- stops a double-tapped submit button becoming two entries, and it is the
-- cheapest brake on a public endpoint that there is.
CREATE UNIQUE INDEX idx_registration_one_per_email
    ON tournament_registration(tournament_id, contact_email)
    WHERE contact_email <> '' AND status <> 'Rejected';

CREATE INDEX idx_registration_tournament ON tournament_registration(tournament_id, status);
