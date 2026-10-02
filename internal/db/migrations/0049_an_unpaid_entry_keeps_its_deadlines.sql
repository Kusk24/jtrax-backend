-- 0049_an_unpaid_entry_keeps_its_deadlines.sql — what an entry was priced
-- as, what the ID card said, and the two dates an unpaid entry answers to.
--
-- A place is saved before the fee is paid (see a-parent-pays-a-tournament-fee),
-- and a family who leaves Stripe may pay at the desk instead. The academy's
-- rule for them: an early-bird price holds only if it is paid by the early-bird
-- date, and a place not paid for by the time registration closes is released.
-- Both are applied by the server (entryrules.go), so each entry has to say
-- whether it was given the early-bird price and whether it was priced as a
-- JCA student — the regular price it falls back to depends on both. Entries
-- made before this have NULL and are left exactly as they were.
--
-- The ID card scan used to be a typing aid only; what it read is now kept
-- beside what was finally submitted, so staff can check an age group against
-- the document when the two disagree.

ALTER TABLE tournament_registration ADD COLUMN early_bird_applied INTEGER;

ALTER TABLE tournament_registration ADD COLUMN priced_as_student INTEGER;

ALTER TABLE tournament_registration ADD COLUMN early_bird_lapsed_at TEXT;

ALTER TABLE tournament_registration ADD COLUMN released_at TEXT;

ALTER TABLE tournament_registration ADD COLUMN participant_name_th TEXT;

ALTER TABLE tournament_registration ADD COLUMN id_document_type TEXT;

ALTER TABLE tournament_registration ADD COLUMN ocr_name TEXT;

ALTER TABLE tournament_registration ADD COLUMN ocr_date_of_birth TEXT;
