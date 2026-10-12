-- 0072_a_student_can_be_archived.sql — a student who leaves the academy is
-- archived, not deleted. Their attendance, payments, credits and results stay;
-- they can no longer sign in, be checked in, or be reminded about credits.
-- Restoring clears both columns.
ALTER TABLE student ADD COLUMN archived_at TEXT;
ALTER TABLE student ADD COLUMN archived_reason TEXT;
