-- 0059_an_unpaid_entry_expires.sql — a tournament fee nobody paid by the
-- deadline says so, and each email about an unpaid entry goes once.
--
-- 1. A payment's status gains Expired: the entry's place was released at the
--    registration deadline with the fee still owed. Pending said "still
--    coming" about money that no longer can, and the desk could not tell the
--    two apart.
--
--    Every public entry with a fee now has its Pending payment from the
--    moment it is made, rather than from the first click on "Pay", so a
--    family who chose to pay later has a payment that can expire too.
--
-- 2. The entry remembers whether the family chose to pay now or later, and
--    when each email about its payment went out. The reminder and the
--    "payment not completed" notice are sent by a timer that runs every few
--    minutes; without a record it would send them every few minutes.
--
-- SQLite cannot widen a CHECK in place, so the payment table is rebuilt the
-- same way as 0018: credit_transaction.payment_id points into it, so those
-- links are parked in a scratch table, nulled, and put back after.

ALTER TABLE tournament_registration ADD COLUMN pay_choice TEXT
    CHECK (pay_choice IN ('now','later'));
ALTER TABLE tournament_registration ADD COLUMN reserved_emailed_at TEXT;
ALTER TABLE tournament_registration ADD COLUMN payment_reminder_sent_at TEXT;
ALTER TABLE tournament_registration ADD COLUMN cancelled_emailed_at TEXT;

-- 3. Every email about an unpaid entry carries its pay link, but only the
--    code's hash is stored, so a later email had no code to put in it. The
--    code is now derived from the entry id with a key that lives here — a
--    table no endpoint reads — and is made on first use.
CREATE TABLE server_secret (
    name  TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

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
    -- Expired: a tournament fee still owed when its place was released.
    -- Like Pending and Refunded, it is not revenue.
    status                     TEXT NOT NULL DEFAULT 'Paid' CHECK (status IN ('Paid','Pending','Refunded','Expired')),
    payment_date               TEXT NOT NULL,
    reference_number           TEXT,
    stripe_session_id          TEXT,
    stripe_checkout_url        TEXT,
    tournament_registration_id TEXT REFERENCES tournament_registration(tournament_registration_id),
    credit_amount              REAL
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
    WHERE tournament_registration_id IS NOT NULL;

UPDATE credit_transaction SET payment_id = (
    SELECT b.payment_id FROM payment_link_backup b
    WHERE b.credit_transaction_id = credit_transaction.credit_transaction_id
)
WHERE credit_transaction_id IN (SELECT credit_transaction_id FROM payment_link_backup);

DROP TABLE payment_link_backup;

-- Places already released before this migration owed a fee that can no
-- longer be paid; their open payments are Expired now, as new ones will be.
UPDATE payment SET status = 'Expired', stripe_session_id = NULL, stripe_checkout_url = NULL
 WHERE status = 'Pending'
   AND tournament_registration_id IN (
       SELECT tournament_registration_id FROM tournament_registration
        WHERE status = 'Withdrawn' AND released_at IS NOT NULL);
