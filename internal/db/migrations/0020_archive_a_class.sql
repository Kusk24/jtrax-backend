-- 0020_archive_a_class.sql — a class the academy no longer runs.
--
-- The console could not remove a class at all: `class_id` is NOT NULL on
-- student_enrollment, class_session and credit_package, so the generic DELETE
-- was refused by the foreign keys the moment anyone had ever been enrolled or
-- a single session had run. The office was stuck with every class it had ever
-- named.
--
-- Deleting the row for real would mean rebuilding those three tables to make
-- class_id nullable — and two of them are themselves referenced by NOT NULL
-- columns (attendance.session_id, credit_transaction.enrollment_id), so their
-- rows would have to be parked and restored mid-migration. That is a great
-- deal of moving parts for a live database, and it buys nothing the office can
-- see.
--
-- So a class is retired instead of destroyed. `archived_at` set means it is
-- gone from every list, picker and form: nobody can be enrolled in it, no
-- session can be created for it, no package sold for it. What it leaves behind
-- keeps working, because the row is still there to be joined to — last term's
-- attendance still names the class it was, and a receipt still says what was
-- bought. Setting the column back to NULL brings it back, which a delete never
-- would.
--
-- One column, no rebuild, nothing to park.

ALTER TABLE class ADD COLUMN archived_at TEXT;

-- Retiring a class is a common lookup on every screen that offers one, and the
-- table is read constantly for names.
CREATE INDEX idx_class_archived ON class(archived_at);
