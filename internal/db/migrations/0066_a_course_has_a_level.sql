-- 0066_a_course_has_a_level.sql — a course is a name, a level and a type.
--
-- JCA runs JCA Starters (Beginner), JCA Juniors (Intermediate) and JCA NXT
-- (Advanced), each taught Private (one-to-one) or Group. Level becomes its
-- own field, the free-text badge goes, and the type is Private or Group —
-- "Master" was a level dressed as a type.
--
-- The test courses are renamed in place, never deleted and recreated: their
-- class_id stays, so every enrolment, package, session, attendance row and
-- credit entry still points at the same course, and nothing reads as
-- removed. Each rename runs only when exactly one active course has the old
-- name and none has the new one, so a database without these test courses,
-- or one already cleaned up, is left alone.

ALTER TABLE class ADD COLUMN level TEXT CHECK (level IN ('Beginner', 'Intermediate', 'Advanced'));

-- Every course gets a level from what it already says about itself: its
-- badge, else a level word in its name, else the old Master type.
UPDATE class SET level = badge WHERE level IS NULL AND badge IN ('Beginner', 'Intermediate', 'Advanced');
UPDATE class SET level = 'Advanced'     WHERE level IS NULL AND (name LIKE '%Advanced%' OR name LIKE '%Master%' OR class_type = 'Master');
UPDATE class SET level = 'Intermediate' WHERE level IS NULL AND name LIKE '%Intermediate%';
UPDATE class SET level = 'Beginner'     WHERE level IS NULL AND name LIKE '%Beginner%';

-- King Slayer → JCA NXT (Advanced, Private). The payments' own copy of the
-- course name follows, so the Payments table names the course as it is now.
UPDATE payment SET class_name = 'JCA NXT'
 WHERE class_name = 'King Slayer'
   AND (SELECT COUNT(*) FROM class WHERE name = 'King Slayer' AND archived_at IS NULL) = 1
   AND NOT EXISTS (SELECT 1 FROM class WHERE name = 'JCA NXT');
UPDATE class SET name = 'JCA NXT', level = 'Advanced', class_type = 'Private'
 WHERE name = 'King Slayer' AND archived_at IS NULL
   AND (SELECT COUNT(*) FROM class WHERE name = 'King Slayer' AND archived_at IS NULL) = 1
   AND NOT EXISTS (SELECT 1 FROM class WHERE name = 'JCA NXT');

-- Summer Challenger → JCA Starters (Beginner, Group).
UPDATE payment SET class_name = 'JCA Starters'
 WHERE class_name = 'Summer Challenger'
   AND (SELECT COUNT(*) FROM class WHERE name = 'Summer Challenger' AND archived_at IS NULL) = 1
   AND NOT EXISTS (SELECT 1 FROM class WHERE name = 'JCA Starters');
UPDATE class SET name = 'JCA Starters', level = 'Beginner', class_type = 'Group'
 WHERE name = 'Summer Challenger' AND archived_at IS NULL
   AND (SELECT COUNT(*) FROM class WHERE name = 'Summer Challenger' AND archived_at IS NULL) = 1
   AND NOT EXISTS (SELECT 1 FROM class WHERE name = 'JCA Starters');

-- Intermediate Chess (Sec 101) → JCA Juniors (Intermediate, Group).
UPDATE payment SET class_name = 'JCA Juniors'
 WHERE class_name = 'Intermediate Chess (Sec 101)'
   AND (SELECT COUNT(*) FROM class WHERE name = 'Intermediate Chess (Sec 101)' AND archived_at IS NULL) = 1
   AND NOT EXISTS (SELECT 1 FROM class WHERE name = 'JCA Juniors');
UPDATE class SET name = 'JCA Juniors', level = 'Intermediate', class_type = 'Group'
 WHERE name = 'Intermediate Chess (Sec 101)' AND archived_at IS NULL
   AND (SELECT COUNT(*) FROM class WHERE name = 'Intermediate Chess (Sec 101)' AND archived_at IS NULL) = 1
   AND NOT EXISTS (SELECT 1 FROM class WHERE name = 'JCA Juniors');

-- "Master" was a second advanced test course beside King Slayer, now JCA
-- NXT. Archived, not deleted: it keeps its history and leaves the pickers.
-- Only when nobody is still actively enrolled in it.
UPDATE class SET archived_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
 WHERE name = 'Master' AND archived_at IS NULL
   AND EXISTS (SELECT 1 FROM class WHERE name = 'JCA NXT')
   AND NOT EXISTS (SELECT 1 FROM student_enrollment e
                    WHERE e.class_id = class.class_id AND e.deleted_date IS NULL AND e.status = 'Active');

-- Master is not a way of teaching: any course still typed so is a group.
UPDATE class SET class_type = 'Group' WHERE class_type = 'Master';

ALTER TABLE class DROP COLUMN badge;
