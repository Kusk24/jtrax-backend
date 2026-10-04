-- 0061_a_package_never_expires_when_blank.sql — one way to say "never".
--
-- Since 0035 a package's validity can be left blank, meaning its credits
-- never expire. 0 had always meant the same downstream, so the form allowed
-- it too, and a package reading "0 days" looked like one that expires at
-- once. The API now refuses 0 (checkPackageValidity); the ones already
-- stored become blank, which is what they always meant.
UPDATE credit_package SET validity_days = NULL WHERE validity_days IS NOT NULL AND validity_days <= 0;
