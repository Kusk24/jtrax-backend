-- 0076_class_charges_to_two_decimals.sql — class charges already written,
-- to two decimals.
--
-- Class charges written with the check-out rounding at 0 kept the exact
-- minutes to fifteen digits (-1.96029527777778), and every balance built on
-- them read the same way. New charges are stored to two decimals at most
-- (credits.go); this brings the ones already written into line. Each moves by
-- less than 0.005 credit. Only class charges: purchases and desk adjustments
-- are amounts staff typed, and are left exactly as typed.
UPDATE credit_transaction
   SET amount = ROUND(amount, 2)
 WHERE transaction_type = 'consumption'
   AND attendance_id IS NOT NULL
   AND amount <> ROUND(amount, 2);
