-- 0028_a_payment_can_carry_a_card_checkout.sql — the Stripe session behind a
-- pending payment.
--
-- The desk records a payment as Pending, generates a card checkout link, and
-- sends it to the parent. The link has to survive the tab closing: the same
-- pending payment asked again must answer with the session it already has,
-- not mint a second one for the parent to pay twice.
--
-- Two columns rather than a table: a payment has at most one live checkout,
-- and regenerating one replaces it. No card number, expiry or holder detail is
-- ever stored anywhere — the checkout happens on Stripe's page, and what comes
-- back is only the session identifier and whether it was paid.

ALTER TABLE payment ADD COLUMN stripe_session_id TEXT;
ALTER TABLE payment ADD COLUMN stripe_checkout_url TEXT;
