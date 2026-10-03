-- 0039 — somebody who registered from the public form can pay for their entry.
--
-- They have no account, so nothing about who they are can decide whether they
-- may pay. What decides it is a secret: a random code handed back when they
-- register and put in the confirmation email's pay link. Only its SHA-256 is
-- stored, the same rule password_reset follows, so this column is not itself a
-- way in. NULL for every entry made before this migration and for every entry
-- made by a parent or the desk, which have their own doors.
ALTER TABLE tournament_registration ADD COLUMN pay_code_hash TEXT;
