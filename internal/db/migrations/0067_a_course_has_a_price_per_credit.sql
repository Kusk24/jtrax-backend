-- 0067_a_course_has_a_price_per_credit.sql — a course's usual price for one
-- credit, so a new package or a custom credit sale starts from a sensible
-- price. Only a starting value: each package and payment keeps its own
-- price, and the office can type any other.
--
-- Filled from the course's most recent current package, where it has one.

ALTER TABLE class ADD COLUMN price_per_credit REAL CHECK (price_per_credit IS NULL OR price_per_credit >= 0);

UPDATE class SET price_per_credit = (
    SELECT ROUND(p.standard_price / p.credit_amount, 2) FROM credit_package p
     WHERE p.class_id = class.class_id AND p.archived_at IS NULL AND p.credit_amount > 0
     ORDER BY p.rowid DESC LIMIT 1)
 WHERE price_per_credit IS NULL;
