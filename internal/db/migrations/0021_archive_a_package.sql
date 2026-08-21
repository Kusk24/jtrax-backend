-- 0021_archive_a_package.sql — a credit package the academy no longer sells.
--
-- The same wall as a class, one table along. `payment.credit_package_id`
-- references this table, so the moment a package has been sold once the
-- generic DELETE is refused and the price list can only ever grow. A term's
-- prices change; the office should be able to retire last term's.
--
-- And the row genuinely cannot go. A payment is what says a family bought
-- twenty credits for 12,000 baht, and the package is where the twenty comes
-- from — the console reads it to show what a receipt bought. Delete it and old
-- receipts stop adding up.
--
-- So the same answer as 0020: retire it. Gone from the till and from the
-- price list, still there for every payment that points at it, and reversible
-- by clearing the column. One column, no rebuild, nothing to park.

ALTER TABLE credit_package ADD COLUMN archived_at TEXT;

CREATE INDEX idx_credit_package_archived ON credit_package(archived_at);
