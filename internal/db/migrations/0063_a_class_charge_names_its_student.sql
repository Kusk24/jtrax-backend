-- 0063_a_class_charge_names_its_student.sql — a class's credit charge carries
-- the student and class it was for, as a purchase does (0026).
--
-- Charges written at check-in named only the enrolment, so anything that
-- reads the ledger by student — the parent's "Credits used" — missed them.
-- New charges name both (credits.go); this fills in the ones already written.

UPDATE credit_transaction
   SET student_id = (SELECT e.student_id FROM student_enrollment e
                      WHERE e.enrollment_id = credit_transaction.enrollment_id),
       class_id = COALESCE(class_id, (SELECT e.class_id FROM student_enrollment e
                      WHERE e.enrollment_id = credit_transaction.enrollment_id))
 WHERE student_id IS NULL AND enrollment_id IS NOT NULL;
