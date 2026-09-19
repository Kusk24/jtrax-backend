-- 0033_a_registration_can_carry_notes.sql — somewhere to put what a parent
-- types about their own child.
--
-- The portal's registration form has always asked for medical notes and
-- remarks, and always thrown both away: there were no columns, so the boxes
-- were uncontrolled and their contents never left the browser. A parent typing
-- an allergy into a form that discards it is worse than a form that never
-- asked — they believe they have told somebody.
--
-- Two columns rather than one free-text field, because they are read by
-- different people at different times: medical notes are what an arbiter or a
-- first-aider needs on the day, remarks are a request for the office.
ALTER TABLE tournament_registration ADD COLUMN medical_notes TEXT NOT NULL DEFAULT '';
ALTER TABLE tournament_registration ADD COLUMN remarks TEXT NOT NULL DEFAULT '';
