-- 0075_a_payment_can_be_voided.sql — a payment recorded in error is voided,
-- never deleted, and every edit and void is kept in its history.
--
-- Voided is not a refund: no money moved back. It says the record was wrong
-- (a duplicate, the wrong amount, the wrong family). It is not revenue, it
-- cannot be edited again, and the credits it bought are taken back by an
-- adjustment that points at it.
--
-- The payment table is rebuilt to widen its status CHECK, as in 0059; the
-- one-payment-per-entry index now ignores voided rows, so a voided entry fee
-- can be recorded again.

CREATE TABLE payment_link_backup AS
SELECT credit_transaction_id, payment_id FROM credit_transaction WHERE payment_id IS NOT NULL;

UPDATE credit_transaction SET payment_id = NULL WHERE payment_id IS NOT NULL;

CREATE TABLE payment_new (
    payment_id                 TEXT PRIMARY KEY,
    student_id                 TEXT REFERENCES student(student_id),
    enrollment_id              TEXT REFERENCES student_enrollment(enrollment_id),
    credit_package_id          TEXT REFERENCES credit_package(credit_package_id),
    student_name               TEXT,
    class_name                 TEXT,
    parent_name                TEXT,
    amount                     REAL NOT NULL,
    discount_amount            REAL NOT NULL DEFAULT 0,
    final_amount               REAL NOT NULL,
    payment_method             TEXT NOT NULL CHECK (payment_method IN ('CreditCard','BankTransfer','Cash','PromptPay')),
    status                     TEXT NOT NULL DEFAULT 'Paid' CHECK (status IN ('Paid','Pending','Refunded','Cancelled','Voided')),
    payment_date               TEXT NOT NULL,
    reference_number           TEXT,
    stripe_session_id          TEXT,
    stripe_checkout_url        TEXT,
    tournament_registration_id TEXT REFERENCES tournament_registration(tournament_registration_id),
    credit_amount              REAL,
    voided_at                  TEXT,
    voided_by                  TEXT,
    void_reason                TEXT
);

INSERT INTO payment_new (
    payment_id, student_id, enrollment_id, credit_package_id,
    student_name, class_name, parent_name,
    amount, discount_amount, final_amount,
    payment_method, status, payment_date, reference_number,
    stripe_session_id, stripe_checkout_url, tournament_registration_id, credit_amount
)
SELECT
    payment_id, student_id, enrollment_id, credit_package_id,
    student_name, class_name, parent_name,
    amount, discount_amount, final_amount,
    payment_method, status, payment_date, reference_number,
    stripe_session_id, stripe_checkout_url, tournament_registration_id, credit_amount
FROM payment;

DROP TABLE payment;

ALTER TABLE payment_new RENAME TO payment;

CREATE INDEX idx_payment_student ON payment(student_id);
CREATE UNIQUE INDEX idx_payment_one_per_registration
    ON payment(tournament_registration_id)
    WHERE tournament_registration_id IS NOT NULL AND status <> 'Voided';

UPDATE credit_transaction SET payment_id = (
    SELECT b.payment_id FROM payment_link_backup b
    WHERE b.credit_transaction_id = credit_transaction.credit_transaction_id
)
WHERE credit_transaction_id IN (SELECT credit_transaction_id FROM payment_link_backup);

DROP TABLE payment_link_backup;

-- What happened to a payment after it was recorded: each edit (the fields it
-- changed, from and to, as JSON) and the void (with its reason). Written by
-- the server only; nothing edits or removes a row here.
CREATE TABLE payment_audit (
    audit_id    TEXT PRIMARY KEY,
    payment_id  TEXT NOT NULL REFERENCES payment(payment_id),
    action      TEXT NOT NULL CHECK (action IN ('edit', 'void')),
    -- The account and its name then, not a foreign key: the history has to
    -- outlive a member of staff's account being removed.
    changed_by       TEXT,
    changed_by_name  TEXT,
    changed_at       TEXT NOT NULL,
    changes     TEXT,
    reason      TEXT
);
CREATE INDEX idx_payment_audit_payment ON payment_audit(payment_id);
