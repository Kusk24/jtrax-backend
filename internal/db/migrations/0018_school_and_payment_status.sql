-- 0018_school_and_payment_status.sql — two fields the console collects and
-- then threw away.
--
-- 1. The registration wizard asks for the child's current school. There was
--    nowhere to put the answer, so it was typed and lost. It matters at the
--    desk: it is how staff tell two children with the same name apart, and how
--    they know which school run a pickup has to fit around.
--
-- 2. A payment's status was a single value. The form offered Paid, Pending and
--    Refunded, and the column would only accept Paid — so a bank transfer that
--    had not cleared and a refund both recorded as money in the till, and
--    Total Revenue counted them. Widening the CHECK is the whole fix; the
--    console already knew the three words.
--
-- SQLite cannot widen a CHECK in place, so the payment table is rebuilt — the
-- same dance as 0006, and for the same reason: credit_transaction.payment_id
-- points into the table being dropped, and DROP TABLE raises one deferred
-- foreign-key violation per referencing row that nothing clears. Park the
-- links in a scratch table, null them, rebuild, put them back.

ALTER TABLE student ADD COLUMN current_school TEXT;

CREATE TABLE payment_link_backup AS
SELECT credit_transaction_id, payment_id FROM credit_transaction WHERE payment_id IS NOT NULL;

UPDATE credit_transaction SET payment_id = NULL WHERE payment_id IS NOT NULL;

CREATE TABLE payment_new (
    payment_id        TEXT PRIMARY KEY,
    student_id        TEXT REFERENCES student(student_id),
    enrollment_id     TEXT REFERENCES student_enrollment(enrollment_id),
    credit_package_id TEXT REFERENCES credit_package(credit_package_id),
    student_name      TEXT,
    class_name        TEXT,
    parent_name       TEXT,
    amount            REAL NOT NULL,
    discount_amount   REAL NOT NULL DEFAULT 0,
    final_amount      REAL NOT NULL,
    payment_method    TEXT NOT NULL CHECK (payment_method IN ('CreditCard','BankTransfer','Cash','PromptPay')),
    -- Pending: promised, not yet in the account. Refunded: it was, and went
    -- back. Neither is revenue, and both have to be recordable or the desk
    -- writes them down as Paid and the books are wrong.
    status            TEXT NOT NULL DEFAULT 'Paid' CHECK (status IN ('Paid','Pending','Refunded')),
    payment_date      TEXT NOT NULL,
    reference_number  TEXT
);

INSERT INTO payment_new (
    payment_id, student_id, enrollment_id, credit_package_id,
    student_name, class_name, parent_name,
    amount, discount_amount, final_amount,
    payment_method, status, payment_date, reference_number
)
SELECT
    payment_id, student_id, enrollment_id, credit_package_id,
    student_name, class_name, parent_name,
    amount, discount_amount, final_amount,
    payment_method, status, payment_date, reference_number
FROM payment;

DROP TABLE payment;

ALTER TABLE payment_new RENAME TO payment;

CREATE INDEX idx_payment_student ON payment(student_id);

UPDATE credit_transaction SET payment_id = (
    SELECT b.payment_id FROM payment_link_backup b
    WHERE b.credit_transaction_id = credit_transaction.credit_transaction_id
)
WHERE credit_transaction_id IN (SELECT credit_transaction_id FROM payment_link_backup);

DROP TABLE payment_link_backup;
