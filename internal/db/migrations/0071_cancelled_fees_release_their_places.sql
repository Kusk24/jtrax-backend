-- An entry whose fee is Cancelled is out of the tournament (tournamentpay.go
-- releasePlace): Withdrawn and stamped released, under Released places, where
-- staff can restore it. Fees cancelled by hand before that rule existed left
-- their entries in the participant list and counted toward the total. This
-- releases those, once; restoring one still brings back its unpaid fee.
UPDATE tournament_registration
   SET status = 'Withdrawn', released_at = datetime('now')
 WHERE status = 'Approved'
   AND EXISTS (SELECT 1 FROM payment p
                WHERE p.tournament_registration_id = tournament_registration.tournament_registration_id
                  AND p.status = 'Cancelled')
   AND NOT EXISTS (SELECT 1 FROM payment p
                    WHERE p.tournament_registration_id = tournament_registration.tournament_registration_id
                      AND p.status = 'Paid');
