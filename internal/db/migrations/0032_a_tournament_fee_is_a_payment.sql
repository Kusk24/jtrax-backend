-- 0032_a_tournament_fee_is_a_payment.sql — what a parent is paying for when
-- they pay for a tournament.
--
-- The entry fee was recorded only on the registration (`fee_charged`), which is
-- a note of what somebody owes and nothing else: no method, no status, no way
-- to say it arrived. So the parent portal's payment step charged nothing, and
-- the desk had no row to reconcile against when it did.
--
-- A tournament fee becomes an ordinary `payment`, the same row the desk already
-- uses for credit packages, with a link back to the registration it settles.
-- Everything downstream then works unchanged: the Stripe webhook marks it Paid,
-- `grantPurchasedCredits` correctly grants nothing (no package, no enrolment),
-- and the payments screen lists it beside the rest of the money.
--
-- The unique index is the guard against a double charge: a parent who taps Pay
-- twice, or reopens the tab, must land on the one payment already created for
-- their registration rather than a second one.
ALTER TABLE payment ADD COLUMN tournament_registration_id TEXT
    REFERENCES tournament_registration(tournament_registration_id);

CREATE UNIQUE INDEX idx_payment_one_per_registration
    ON payment(tournament_registration_id)
    WHERE tournament_registration_id IS NOT NULL;
