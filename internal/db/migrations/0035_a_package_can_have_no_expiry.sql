-- 0035_a_package_can_have_no_expiry.sql — validity_days becomes optional.
--
-- `credit_package.validity_days` was NOT NULL, so every package had to name a
-- window even when the office meant "these never run out" — a founding
-- member's rate, a free trial, a make-good after an outage. The only way to
-- say that was to type 0 and hope everyone downstream agreed 0 means forever.
--
-- They already did, which is the tell that NOT NULL was the mistake, not the
-- missing feature: `grantPurchasedCredits` in stripe.go has read this column
-- as `COALESCE(validity_days, 0)` since it was written, and the console's
-- `expiryFrom` has treated a falsy validity the same way — both already
-- anticipated NULL, on a column that could not yet hold one.
--
-- NULL now means what 0 already meant, and reads better: a package whose
-- validity was never answered is not "answered with zero days", it is
-- unanswered. `Number(row.validity_days ?? 0)` on the read side treats NULL
-- and 0 identically, so nothing downstream had to change to accept it.
--
-- `payment.credit_package_id` points into the table being rebuilt, the same
-- wrinkle 0006 hit: DROP TABLE raises one deferred-FK violation per
-- referencing row and nothing clears it before COMMIT, so `defer_foreign_keys`
-- does not rescue it. The fix is the same one 0006 used — park the links,
-- null them, rebuild, put them back, so no reference is ever left dangling.

CREATE TABLE credit_package_link_backup AS
SELECT payment_id, credit_package_id FROM payment WHERE credit_package_id IS NOT NULL;

UPDATE payment SET credit_package_id = NULL WHERE credit_package_id IS NOT NULL;

CREATE TABLE credit_package_new (
    credit_package_id TEXT PRIMARY KEY,
    class_id          TEXT NOT NULL REFERENCES class(class_id),
    credit_amount     REAL NOT NULL,
    standard_price    REAL NOT NULL,
    validity_days     INTEGER,
    archived_at       TEXT
);

INSERT INTO credit_package_new (
    credit_package_id, class_id, credit_amount, standard_price, validity_days, archived_at
)
SELECT credit_package_id, class_id, credit_amount, standard_price, validity_days, archived_at
FROM credit_package;

DROP TABLE credit_package;

ALTER TABLE credit_package_new RENAME TO credit_package;

CREATE INDEX idx_credit_package_archived ON credit_package(archived_at);

UPDATE payment
SET credit_package_id = (
    SELECT b.credit_package_id FROM credit_package_link_backup b WHERE b.payment_id = payment.payment_id
)
WHERE payment_id IN (SELECT payment_id FROM credit_package_link_backup);

DROP TABLE credit_package_link_backup;
