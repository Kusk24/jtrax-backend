-- 0079_a_parent_can_be_archived.sql — a guardian whose children have all
-- left is archived with them.
--
-- Archiving a student offers to archive a guardian who has no other child
-- here. Like an archived student, an archived guardian keeps everything on
-- record — payments, contacts, history — but no longer signs in or receives
-- the academy's announcements. Restoring the student restores them.
ALTER TABLE parent ADD COLUMN archived_at TEXT;
