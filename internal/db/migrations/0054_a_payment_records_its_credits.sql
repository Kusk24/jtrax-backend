-- A payment records how many credits it bought.
--
-- Credits used to be read off the payment's package. The desk can now sell a
-- custom number of credits with no package behind it, so the payment carries
-- the count itself. Older rows leave it NULL and still read from the package.
ALTER TABLE payment ADD COLUMN credit_amount REAL;
