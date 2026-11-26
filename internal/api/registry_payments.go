// Credit packages, payments and credit transactions served by the shared CRUD API.
package api

import (
	"github.com/Kusk24/jtrax-backend/internal/auth"
	"github.com/Kusk24/jtrax-backend/internal/payments"
	"github.com/Kusk24/jtrax-backend/internal/records"
	"github.com/Kusk24/jtrax-backend/internal/tournaments"
)

func paymentResources() []*records.Resource {
	return []*records.Resource{
		{
			Name: "credit-packages", Table: "credit_package", IDCol: "credit_package_id", IDPrefix: "pkg",
			Cols: []records.Col{
				{Name: "class_id", Kind: "text", Required: true},
				{Name: "credit_amount", Kind: "real", Required: true},
				{Name: "standard_price", Kind: "real", Required: true},
				// Optional since 0035: a package the office never wants to
				// expire — a founding rate, a free trial — has nothing
				// truthful to put here. Absent reads as "never", the same as
				// 0 always has downstream (grantPurchasedCredits, expiryFrom).
				{Name: "validity_days", Kind: "int"},
				// Set when the academy stops selling this package. Payments
				// point at it and a receipt has to keep saying what it bought,
				// so the row stays and only the till forgets. See 0021.
				{Name: "archived_at", Kind: "text"},
			},
			ReadRoles: everyone,
			// Blank is "never expires"; 0 is refused rather than read as the
			// same, because "0 days" says the opposite to whoever reads it.
			Check: payments.CheckPackageValidity,
		},
		{
			Name: "payments", Table: "payment", IDCol: "payment_id", IDPrefix: "pay",
			// A payment is the record of money taken; it is corrected, never
			// removed, so the history a family and the books see stays whole.
			NoDelete:     "payments can't be deleted — edit or void the payment instead",
			AfterWrite:   tournaments.ReleaseCancelledEntry,
			BeforeUpdate: payments.AuditPaymentEdit,
			Cols: []records.Col{
				// Not required: a payment outlives the student it was taken
				// for, and a detached one carries the names below instead.
				{Name: "student_id", Kind: "text"},
				{Name: "enrollment_id", Kind: "text"},
				{Name: "credit_package_id", Kind: "text"},
				// Credits this payment bought (migration 0054): a package's
				// count, or a custom number the desk typed in.
				{Name: "credit_amount", Kind: "real"},
				// Snapshots of who and what, written when the money changed
				// hands. Renaming a class later must not rewrite old receipts.
				{Name: "student_name", Kind: "text"},
				{Name: "class_name", Kind: "text"},
				{Name: "parent_name", Kind: "text"},
				{Name: "amount", Kind: "real", Required: true},
				{Name: "discount_amount", Kind: "real"},
				{Name: "final_amount", Kind: "real", Required: true},
				{Name: "payment_method", Kind: "text", Enum: payMethods, Required: true},
				{Name: "status", Kind: "text", Enum: payStatus},
				{Name: "payment_date", Kind: "text", Required: true},
				{Name: "reference_number", Kind: "text"},
				// Set when the money is a tournament entry fee rather than a
				// credit package. The portal reads it to tell a family which
				// of their registrations is still owed for.
				{Name: "tournament_registration_id", Kind: "text"},
			},
			// Shown, never written here: voiding is its own act (paymentvoid.go).
			Derived: []records.Derived{
				{Name: "voided_at", Expr: "payment.voided_at", Roles: []string{"Parent", "Student"}},
				{Name: "void_reason", Expr: "payment.void_reason"},
			},
			ReadRoles: []string{"Parent", "Student"},
			Scope: map[string]records.ScopeFn{
				"Parent":  byParentStudents("student_id"),
				"Student": byOwnStudent("student_id"),
			},
		},
		{
			Name: "credit-transactions", Table: "credit_transaction", IDCol: "credit_transaction_id", IDPrefix: "ctx",
			Cols: []records.Col{
				// Not required since 0026: a credit with no enrolment is one
				// the child holds and has not spent anywhere yet, which is
				// what is left when their last enrolment is deleted.
				{Name: "enrollment_id", Kind: "text"},
				// Who the hours belong to, and what they were bought for. The
				// enrolment used to answer both by proxy, which worked only
				// while there was one.
				{Name: "student_id", Kind: "text"},
				{Name: "class_id", Kind: "text"},
				{Name: "transaction_type", Kind: "text", Enum: creditTxTypes, Required: true},
				{Name: "amount", Kind: "real", Required: true},
				{Name: "expiry_date", Kind: "text"},
				{Name: "transaction_date", Kind: "text", Required: true},
				{Name: "payment_id", Kind: "text"},
				{Name: "attendance_id", Kind: "text"},
				{Name: "notes", Kind: "text"},
			},
			ReadRoles: []string{"Parent", "Student"},
			// Reached by the enrolment *or* by the student on the row. Since
			// 0026 a credit can outlive its enrolment, and matching only
			// through `student_enrollment` would have made a family's
			// remaining balance disappear from their own portal the moment the
			// office tidied away the course it was bought for. `student_id` is
			// null on nothing the backfill could resolve, so the first arm
			// still carries the rows written before that migration.
			Scope: map[string]records.ScopeFn{
				"Parent": func(id *auth.Identity) (string, []any) {
					return `(enrollment_id IN (SELECT enrollment_id FROM student_enrollment
						WHERE student_id IN (SELECT student_id FROM student_parent WHERE parent_id = ?))
						OR student_id IN (SELECT student_id FROM student_parent WHERE parent_id = ?))`,
						[]any{id.ParentID, id.ParentID}
				},
				"Student": func(id *auth.Identity) (string, []any) {
					return `(enrollment_id IN (SELECT enrollment_id FROM student_enrollment WHERE student_id = ?)
						OR student_id = ?)`, []any{id.StudentID, id.StudentID}
				},
			},
		},
	}
}
