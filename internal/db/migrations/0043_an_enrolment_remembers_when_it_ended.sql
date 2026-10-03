-- An enrolment remembers the day it ended.
--
-- A child's course history reads "Moved Beginner → Intermediate on 12 Sep"
-- from the new enrolment's own start date and moved_from_class_id. A course
-- that was simply left had no date at all — status turned Withdrawn and
-- nothing said when. The console writes this day when it ends an enrolment;
-- rows that ended before this migration stay undated, since nothing recorded
-- when.
ALTER TABLE student_enrollment ADD COLUMN ended_date TEXT;
