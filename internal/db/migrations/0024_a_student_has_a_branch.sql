-- 0024_a_student_has_a_branch.sql — where a child is enrolled.
--
-- The console has shown a Branch on every student since it was built, and it
-- was never stored: `toStudents` wrote the literal "Bangkok" onto every row,
-- and the edit form offered a picker with one option and nowhere to put the
-- answer. Registration never asked at all.
--
-- The academy has one branch today and expects more, and the front desk needs
-- to record which one a child belongs to at the moment they register rather
-- than after a second site exists and the answer has to be reconstructed for
-- everyone already on the roster.
--
-- Deliberately a plain TEXT with no CHECK. The list of branches is the
-- academy's to grow, and a CHECK would mean a migration every time they open
-- one — the same trap 0018 had to unpick for payment.status. Existing rows are
-- backfilled to Bangkok because that is where every one of them is: it is the
-- only branch that has ever existed.

ALTER TABLE student ADD COLUMN branch TEXT;

UPDATE student SET branch = 'Bangkok' WHERE branch IS NULL;
