-- The academy accepts every tournament entry, so approval is gone.
--
-- Rows already waiting have to go somewhere. Leaving them Pending would leave
-- them in a state with no screen left to resolve it: the queue that used to
-- empty it has become a roster, so a person who registered last week would sit
-- there forever, neither in the tournament nor turned away.
--
-- Approved is the honest reading. Under the old rule these were entries nobody
-- had objected to yet, and under the new rule nobody would have.
--
-- fee_charged comes with it. Approving was where the quote stopped being a
-- quote and became a charge, and these rows never reached that step, so their
-- fee_charged is NULL — which on the desk's roster reads as owing nothing.
-- COALESCE rather than a blanket assignment: a fee a member of staff has
-- already typed in by hand is not one this migration should overwrite.
UPDATE tournament_registration
   SET status      = 'Approved',
       fee_charged = COALESCE(fee_charged, fee_quoted)
 WHERE status = 'Pending';

-- Rejected and Withdrawn rows are left exactly as they are. They are the record
-- of a decision that was really made, and the CHECK constraint still allows
-- both — Withdrawn is how the desk takes somebody out of a tournament now, and
-- Rejected stays legible rather than being rewritten into something it wasn't.
