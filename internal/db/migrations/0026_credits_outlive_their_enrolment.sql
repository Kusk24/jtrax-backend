-- 0026_credits_outlive_their_enrolment.sql — hours a family paid for belong to
-- the child, not to the row that happened to record them.
--
-- `credit_transaction.enrollment_id` was NOT NULL, so a credit could not exist
-- without an enrolment. Delete a child's only enrolment and their balance went
-- with it: thirteen credits, paid for, gone to zero, with no way to put them
-- back when the child enrolled again. The console had to choose between
-- refusing the delete and destroying the ledger, and neither is the answer —
-- the money was real either way.
--
-- Three changes, and the last one is the point:
--
--   * `enrollment_id` becomes nullable. A credit with no enrolment is a credit
--     the child holds and has not yet spent anywhere.
--   * `student_id` is added, because that is who the credit was always for.
--     Reading it through the enrolment worked only while there was one.
--   * `class_id` is added: the course the hours were bought for. A rate is
--     price-per-credit of a course's package, so without this a detached
--     balance cannot be converted into anything — it would move as a bare
--     number and quietly change what a family paid for.
--
-- Both new columns are backfilled from the enrolment, which is where the
-- answers have been all along. They are nullable rather than NOT NULL: a
-- future row could be written before its student is known, and a constraint
-- that has to be true forever is a promise this table cannot make.
--
-- A plain rebuild, unlike 0006 and 0018 — nothing in the schema references
-- credit_transaction, so there are no deferred foreign keys to park and
-- restore. `payment_id` and `attendance_id` point outward and are unaffected.

CREATE TABLE credit_transaction_new (
    credit_transaction_id TEXT PRIMARY KEY,
    -- Null once the enrolment it was spent against is gone. The credit stays.
    enrollment_id         TEXT REFERENCES student_enrollment(enrollment_id),
    -- Who the hours belong to, which an enrolment used to answer by proxy.
    student_id            TEXT REFERENCES student(student_id),
    -- What they were bought for, so a detached balance still has a rate.
    class_id              TEXT REFERENCES class(class_id),
    transaction_type      TEXT NOT NULL CHECK (transaction_type IN ('purchase','consumption','manual_adjustment')),
    amount                REAL NOT NULL,
    expiry_date           TEXT,
    transaction_date      TEXT NOT NULL,
    payment_id            TEXT REFERENCES payment(payment_id),
    attendance_id         TEXT REFERENCES attendance(attendance_id),
    notes                 TEXT
);

INSERT INTO credit_transaction_new (
    credit_transaction_id, enrollment_id, student_id, class_id,
    transaction_type, amount, expiry_date, transaction_date,
    payment_id, attendance_id, notes
)
SELECT
    ct.credit_transaction_id,
    ct.enrollment_id,
    (SELECT e.student_id FROM student_enrollment e WHERE e.enrollment_id = ct.enrollment_id),
    (SELECT e.class_id   FROM student_enrollment e WHERE e.enrollment_id = ct.enrollment_id),
    ct.transaction_type, ct.amount, ct.expiry_date, ct.transaction_date,
    ct.payment_id, ct.attendance_id, ct.notes
FROM credit_transaction ct;

DROP TABLE credit_transaction;

ALTER TABLE credit_transaction_new RENAME TO credit_transaction;

CREATE INDEX idx_credit_tx_enrollment ON credit_transaction(enrollment_id);
-- A child's balance is now read by student, including the entries no enrolment
-- claims, so this is the index the roster actually uses.
CREATE INDEX idx_credit_tx_student ON credit_transaction(student_id);
