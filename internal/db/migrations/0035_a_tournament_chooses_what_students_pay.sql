-- 0035_a_tournament_chooses_what_students_pay.sql — which of the two prices a
-- JCA student is offered.
--
-- A tournament has always had two ways to be cheaper: the student discount
-- (0014) and the early-bird price (0029). Until now the rule between them was
-- fixed in code — a student got the discount off the regular fee and never the
-- early-bird price, because the two were meant for different people. The
-- organiser now decides per event: the discount, early bird, both stacked, or
-- neither.
--
-- The defaults are the old rule, so every tournament that already exists
-- charges a student exactly what it charged yesterday.
ALTER TABLE tournament ADD COLUMN student_gets_discount INTEGER NOT NULL DEFAULT 1
    CHECK (student_gets_discount IN (0, 1));
ALTER TABLE tournament ADD COLUMN student_gets_early_bird INTEGER NOT NULL DEFAULT 0
    CHECK (student_gets_early_bird IN (0, 1));
