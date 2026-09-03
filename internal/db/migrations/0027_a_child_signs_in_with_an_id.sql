-- 0027_a_child_signs_in_with_an_id.sql — retire the invented student mailbox.
--
-- Every student account the console has ever created was given an address at
-- `@student.jca.ac.th`: `penny.ward@student.jca.ac.th`. That domain receives no
-- mail and never has. The address was only ever a username wearing an
-- address's clothes — which the code even said out loud, in a comment beside
-- the parent version of the same trick.
--
-- The cost of the disguise is not cosmetic. A five-year-old has no mailbox, so
-- every feature that reasonably assumes an address can be written to is wrong
-- about these rows: the password-reset link goes into the void, and an office
-- looking at the field has no way to tell the fiction from a real address a
-- family actually gave. `stu_penny_ward` cannot be mistaken for a promise to
-- deliver anything.
--
-- Only the invented domain is converted. A student who gave a real address
-- keeps it — that one can receive a reset link, and taking it away would lock
-- a child out of the only self-service route they have. The rule is
-- deliberately about the *domain*, not the role: it is the exact set of rows
-- this console minted, and nothing else.
--
-- Passwords are untouched, so nobody is signed out and nothing needs reissuing.
-- What changes is the string typed into the first box.

UPDATE user_account
SET email = 'stu_' || replace(replace(substr(email, 1, instr(email, '@') - 1), '.', '_'), '-', '_')
WHERE role = 'Student'
  AND email LIKE '%@student.jca.ac.th'
  -- Not already taken by somebody else. Two guards rather than one, because
  -- the UNIQUE index turns a collision into a failed migration, and a failed
  -- migration is a failed deploy: this has to decline rather than explode.
  AND NOT EXISTS (
      SELECT 1 FROM user_account other
      WHERE other.user_account_id <> user_account.user_account_id
        AND other.email = 'stu_' || replace(replace(substr(user_account.email, 1, instr(user_account.email, '@') - 1), '.', '_'), '-', '_')
  )
  -- And no *second* row on its way to the same ID. `a.b@` and `a-b@` both
  -- flatten to `stu_a_b`, so two rows that look distinct today can land on one
  -- string. Neither is converted; both keep their address and show up as an
  -- account still on the old domain, which is a thing the office can see and
  -- decide about. Merging two children is not a migration's call.
  AND NOT EXISTS (
      SELECT 1 FROM user_account twin
      WHERE twin.user_account_id <> user_account.user_account_id
        AND twin.role = 'Student'
        AND twin.email LIKE '%@student.jca.ac.th'
        AND replace(replace(substr(twin.email, 1, instr(twin.email, '@') - 1), '.', '_'), '-', '_')
          = replace(replace(substr(user_account.email, 1, instr(user_account.email, '@') - 1), '.', '_'), '-', '_')
  );
