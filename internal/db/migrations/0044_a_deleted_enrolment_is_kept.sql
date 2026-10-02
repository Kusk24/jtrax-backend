-- Deleting an enrolment keeps it, marked.
--
-- Delete used to erase the row, and with it the only record that the child was
-- ever in that course — the office asked for the student's course list to keep
-- everything that was done, deletions included. A deleted enrolment is now
-- Withdrawn with this day set: the console lists it under All as "Deleted",
-- nothing charges it, and the family's apps no longer see it.
ALTER TABLE student_enrollment ADD COLUMN deleted_date TEXT;
