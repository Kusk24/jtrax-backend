-- 0078_release_emails_of_deleted_people.sql — the emails of parents and
-- students already deleted, given back.
--
-- Deleting a parent or student kept their login whenever anything still
-- pointed at it — a notification they had been sent was enough — and the
-- login kept its email, so the office could not register anyone new with that
-- address ("already used by another account"). Deleting now releases it
-- (people.go); this does the same for the logins left behind before: a parent
-- or student login with no parent or student behind it gets an address no one
-- can own, and a password hash nothing matches. The row stays for the history
-- that points at it.
UPDATE user_account
   SET email = 'deleted+' || user_account_id || '@deleted.invalid',
       password_hash = '!'
 WHERE role IN ('Parent', 'Student')
   AND email NOT LIKE 'deleted+%@deleted.invalid'
   AND NOT EXISTS (SELECT 1 FROM parent p WHERE p.user_account_id = user_account.user_account_id)
   AND NOT EXISTS (SELECT 1 FROM student s WHERE s.user_account_id = user_account.user_account_id);

DELETE FROM auth_session
 WHERE user_account_id IN (SELECT user_account_id FROM user_account WHERE email LIKE 'deleted+%@deleted.invalid');
