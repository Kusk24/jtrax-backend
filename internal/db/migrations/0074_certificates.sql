-- 0074_certificates.sql — certificates by course.
--
-- A class belongs to one of the academy's certificate courses — JCA Starters,
-- JCA Juniors, JCA NXT — whether it is taught privately or in a group, so the
-- hours of both count toward the same certificate. Classes outside the three
-- (a camp, a one-off) have none. Filled from the class name once; the office
-- sets it on the course after that.
--
-- A certificate is issued by the office once the student has the hours and has
-- passed the assessment, which happens outside JTrax. One per student and
-- course, ever.
ALTER TABLE class ADD COLUMN certificate_course TEXT
    CHECK (certificate_course IN ('starters', 'juniors', 'nxt'));

UPDATE class SET certificate_course = CASE
    WHEN lower(name) LIKE '%starter%' THEN 'starters'
    WHEN lower(name) LIKE '%junior%'  THEN 'juniors'
    WHEN lower(name) LIKE '%nxt%'     THEN 'nxt'
END;

CREATE TABLE student_certificate (
    certificate_id         TEXT PRIMARY KEY,
    student_id             TEXT NOT NULL REFERENCES student(student_id),
    course                 TEXT NOT NULL CHECK (course IN ('starters', 'juniors', 'nxt')),
    certified_at           TEXT NOT NULL,
    certified_by           TEXT REFERENCES user_account(user_account_id),
    hours_at_certification REAL NOT NULL,
    UNIQUE (student_id, course)
);
