-- 0019_backfill_session_hours.sql — give every session that already exists the
-- length it was always running for.
--
-- One credit is one hour, so an attendance costs what its session lasts. That
-- reads `class_session.duration_hours`, which until now only the seed and the
-- roster import ever set: the console's session form collects a start time and
-- an end time and nothing worked out the difference, so every session staff
-- created carried a NULL length. New writes fill it in from the clock; this is
-- the ones already on file.
--
-- `strftime('%s', ...)` needs a date to anchor the time, hence the constant
-- day — only the difference is used, so which day it is does not matter. Rows
-- whose times are unreadable or run backwards are left NULL rather than given
-- a wrong number: an incomplete timetable should stay visibly incomplete.

UPDATE class_session
SET duration_hours = (
    strftime('%s', '2000-01-01 ' || end_time) - strftime('%s', '2000-01-01 ' || start_time)
) / 3600.0
WHERE (duration_hours IS NULL OR duration_hours <= 0)
  AND start_time IS NOT NULL
  AND end_time IS NOT NULL
  AND strftime('%s', '2000-01-01 ' || end_time) IS NOT NULL
  AND strftime('%s', '2000-01-01 ' || start_time) IS NOT NULL
  AND strftime('%s', '2000-01-01 ' || end_time) > strftime('%s', '2000-01-01 ' || start_time);
