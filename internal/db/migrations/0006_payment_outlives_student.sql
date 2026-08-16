-- 0006_payment_outlives_student.sql — a payment is a financial record and has
-- to survive the student it was taken for.
--
-- Deleting a child used to delete their payments with them, which loses the
-- academy's own books: the money was received, and "who paid what" is the one
-- thing that must still be answerable a year later. Two changes make that
-- possible:
--
--   * student_id becomes nullable, so the row can be detached from a student
--     who no longer exists rather than deleted with them;
--   * the names are snapshotted onto the payment, so a detached row still says
--     which child it was for, which class, and which guardian paid.
--
-- Snapshots rather than joins on purpose. A payment records what was true when
-- the money changed hands — renaming a class later must not rewrite last
-- year's receipts.
--
-- SQLite cannot drop NOT NULL in place, so this is the documented table
-- rebuild, with one wrinkle: credit_transaction.payment_id points into the
-- table being dropped. `PRAGMA defer_foreign_keys` does NOT rescue that —
-- DROP TABLE raises one deferred violation per referencing row and nothing
-- clears them, so COMMIT fails. The fix is to make sure no reference is
-- dangling at any point: park the links in a scratch table, null them, rebuild,
-- then put them back. Deterministic, and it needs no pragma at all.

CREATE TABLE payment_link_backup AS
SELECT credit_transaction_id, payment_id FROM credit_transaction WHERE payment_id IS NOT NULL;

UPDATE credit_transaction SET payment_id = NULL WHERE payment_id IS NOT NULL;

CREATE TABLE payment_new (
    payment_id        TEXT PRIMARY KEY,
    -- Nullable: NULL means the student was deleted and the names below are the
    -- only record of who this was for.
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
    status            TEXT NOT NULL DEFAULT 'Paid' CHECK (status IN ('Paid')),
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
    p.payment_id, p.student_id, p.enrollment_id, p.credit_package_id,
    (SELECT s.name FROM student s WHERE s.student_id = p.student_id),
    (SELECT c.name FROM class c
        JOIN student_enrollment e ON e.class_id = c.class_id
        WHERE e.enrollment_id = p.enrollment_id),
    (SELECT pr.name FROM parent pr
        JOIN student_parent sp ON sp.parent_id = pr.parent_id
        WHERE sp.student_id = p.student_id),
    p.amount, p.discount_amount, p.final_amount,
    p.payment_method, p.status, p.payment_date, p.reference_number
FROM payment p;

DROP TABLE payment;

ALTER TABLE payment_new RENAME TO payment;

CREATE INDEX idx_payment_student ON payment(student_id);

UPDATE credit_transaction SET payment_id = (
    SELECT b.payment_id FROM payment_link_backup b
    WHERE b.credit_transaction_id = credit_transaction.credit_transaction_id
)
WHERE credit_transaction_id IN (SELECT credit_transaction_id FROM payment_link_backup);

DROP TABLE payment_link_backup;
