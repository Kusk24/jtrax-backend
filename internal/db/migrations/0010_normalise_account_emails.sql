-- 0010_normalise_account_emails.sql — lower-case any address that is not.
--
-- Sign-in lower-cases what the user types before matching, but the UNIQUE index
-- on user_account.email is byte-exact. So a row that ever got in as
-- `Head@JCA.ac.th` — from a direct SQL edit, an import, or an older build —
-- can never be signed in to. The account exists, the password is right, and the
-- login fails, which is a very confusing thing to debug from the outside.
--
-- Every write path in the application normalises now, so this repairs history
-- rather than compensating for an ongoing bug.
--
-- The NOT EXISTS guard matters: if two accounts differ only in case, lowering
-- one collides with the other and the whole migration would fail. Those rows
-- are deliberately left alone, because merging two accounts is a decision about
-- which one's data survives, and no migration should make it silently. They
-- will show up as an address that still has capitals in it.
UPDATE user_account
SET email = lower(trim(email))
WHERE email <> lower(trim(email))
  AND NOT EXISTS (
      SELECT 1 FROM user_account other
      WHERE other.email = lower(trim(user_account.email))
        AND other.user_account_id <> user_account.user_account_id
  );

-- The same for the contact addresses staff record by hand, which feed nothing
-- security-critical but are read back into forms and compared against.
UPDATE teacher SET email = lower(trim(email)) WHERE email IS NOT NULL AND email <> lower(trim(email));
UPDATE admin   SET email = lower(trim(email)) WHERE email IS NOT NULL AND email <> lower(trim(email));
UPDATE parent_contact
SET value = lower(trim(value))
WHERE contact_type = 'email' AND value <> lower(trim(value));
