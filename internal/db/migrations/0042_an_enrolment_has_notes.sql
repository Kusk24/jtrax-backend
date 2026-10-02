-- An enrolment carries a note.
--
-- The office keeps things about one child's place in one course that belong
-- nowhere else — "paid the rest in cash, receipt to follow", "moving to
-- Saturdays after the exam". A credit entry's note is about that purchase; this
-- is about the enrolment as a whole.
ALTER TABLE student_enrollment ADD COLUMN notes TEXT NOT NULL DEFAULT '';
