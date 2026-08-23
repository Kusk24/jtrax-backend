-- 0023_a_teacher_needs_no_login.sql — a teacher is a record, not an account.
--
-- The academy has no teacher workflow. Attendance is taken by the front desk,
-- credits are spent by the desk, and everything a teacher would have done in
-- software is done in the console by an admin or a receptionist. There is no
-- teacher portal in any of the three front-ends, and none is planned.
--
-- But the academy still employs teachers, and still has to say who teaches
-- what: the Academy screen lists them, and a parent's class card names the one
-- their child is with. So the row stays and the login goes.
--
-- `user_account_id` was NOT NULL UNIQUE REFERENCES user_account, which forced
-- every teacher record to come with a credential. The console duly minted one —
-- a fabricated @jca.ac.th address and a random password that nobody was ever
-- shown — so the academy accumulated live logins for a portal that does not
-- exist. That is an authentication surface kept open for no purpose.
--
-- Nullable now. A teacher written from here on has no account at all; the ones
-- that already exist keep the link, so nothing on file changes and no existing
-- sign-in breaks. UNIQUE is kept and still does its job — SQLite allows many
-- NULLs in a unique column, so several account-less teachers coexist while a
-- linked one is still one-to-one with its account.
--
-- Rebuilt rather than altered because SQLite cannot drop a NOT NULL in place.
-- Safe to do plainly: nothing in the schema references `teacher` — no
-- `teacher_id` foreign key exists anywhere — so there are no children to park,
-- which is what made 0020 choose archiving over a rebuild. This one has none of
-- that problem.

CREATE TABLE teacher_new (
    teacher_id      TEXT PRIMARY KEY,
    user_account_id TEXT UNIQUE REFERENCES user_account(user_account_id),
    name            TEXT NOT NULL,
    phone           TEXT,
    email           TEXT,
    line_id         TEXT
);

INSERT INTO teacher_new (teacher_id, user_account_id, name, phone, email, line_id)
SELECT teacher_id, user_account_id, name, phone, email, line_id FROM teacher;

DROP TABLE teacher;

ALTER TABLE teacher_new RENAME TO teacher;
