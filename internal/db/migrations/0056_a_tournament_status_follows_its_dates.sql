-- A tournament's status follows its dates.
--
-- Nothing ever moved tournament_status on: every tournament stayed as it was
-- created. It is now worked out from start_date and end_date — Upcoming, then
-- Ongoing, then Completed — unless the office sets it by hand, which sets
-- status_locked and leaves it alone (an event called off, or ended early).
ALTER TABLE tournament ADD COLUMN status_locked INTEGER NOT NULL DEFAULT 0
    CHECK (status_locked IN (0, 1));
