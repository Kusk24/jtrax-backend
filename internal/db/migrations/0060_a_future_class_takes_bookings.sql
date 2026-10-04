-- 0060_a_future_class_takes_bookings.sql — students can be put on a class
-- that has not started, without paying for it yet.
--
-- A class's roster was its attendance, and an attendance row is charged the
-- moment it is written (credits.go) — being on the roster meant being there.
-- So a class made for next week could only be filled once it was running, or
-- the families saw credits leave for a lesson nobody had had.
--
-- A booking is "will be in this class". It costs nothing. When the class
-- starts, the server turns each booking into an attendance row through the
-- same checks and the same charge as a check-in at the desk (classstart.go),
-- and the booking goes. A booking that could not become attendance — the
-- credits had expired, say — stays, with the reason, for the desk to settle.

CREATE TABLE session_booking (
    booking_id     TEXT PRIMARY KEY,
    session_id     TEXT NOT NULL REFERENCES class_session(session_id),
    student_id     TEXT NOT NULL REFERENCES student(student_id),
    booked_at      TEXT NOT NULL DEFAULT (datetime('now')),
    -- Why it did not become attendance at the start, when it did not.
    failed_reason  TEXT,
    UNIQUE (session_id, student_id)
);

CREATE INDEX idx_session_booking_student ON session_booking(student_id);

-- A class made for later is Scheduled, not Ongoing, until it starts.
UPDATE class_session SET session_status = 'Scheduled'
 WHERE session_status = 'Ongoing' AND cancelled_at IS NULL
   AND session_date > date('now', '+7 hours');
