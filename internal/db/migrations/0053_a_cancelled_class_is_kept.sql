-- Cancelling a class keeps it, marked.
--
-- Cancel used to delete the session, so Today's Classes lost every trace of a
-- class the office had called off and the desk could not tell "cancelled" from
-- "never scheduled". The session now stays with the moment it was cancelled:
-- its attendance is still refunded and removed, staff see it as Cancelled, and
-- teachers, parents and students no longer see it at all.
ALTER TABLE class_session ADD COLUMN cancelled_at TEXT;
