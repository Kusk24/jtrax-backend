// Credit reminders and notifications.
package payments

import (
	"database/sql"
	"time"

	"github.com/Kusk24/jtrax-backend/internal/auth"
	"github.com/Kusk24/jtrax-backend/internal/entrymail"
	"github.com/Kusk24/jtrax-backend/internal/families"
	"github.com/Kusk24/jtrax-backend/internal/fees"
	"github.com/Kusk24/jtrax-backend/internal/notify"
	"github.com/Kusk24/jtrax-backend/internal/records"
)

// PaymentHook notifies the guardian when a payment lands as Paid — whether
// the desk recorded it Paid outright, marked a pending one Paid later, or the
// Stripe webhook settled it (which calls notifyPaymentPaid itself, since it
// writes SQL directly rather than through the resource). The dedupe key is the
// payment, so edits to a settled row never re-notify.
func PaymentHook(svc *notify.Service) func(*sql.DB, *auth.Identity, map[string]any, bool) {
	return func(d *sql.DB, _ *auth.Identity, row map[string]any, _ bool) {
		if records.StringValue(row, "status") != "Paid" {
			return
		}
		NotifyPaymentPaid(d, svc, records.StringValue(row, "payment_id"))
	}
}

// NotifyPaymentPaid sends the PDF's "Payment Successful" message: the amount,
// the credits it bought, and the balance they leave. In-app and email both —
// a receipt is the one notification the academy wants in writing.
func NotifyPaymentPaid(d *sql.DB, svc *notify.Service, paymentID string) {
	if paymentID == "" {
		return
	}
	var studentID, enrolmentID, regID sql.NullString
	var amount float64
	var forWhat, method, paidOn, reference string
	if err := d.QueryRow(
		`SELECT student_id, enrollment_id, final_amount, tournament_registration_id,
		        COALESCE(class_name, ''), COALESCE(payment_method, ''), COALESCE(payment_date, ''),
		        COALESCE(reference_number, '')
		   FROM payment WHERE payment_id = ?`,
		paymentID).Scan(&studentID, &enrolmentID, &amount, &regID, &forWhat, &method, &paidOn, &reference); err != nil {
		return
	}
	// A tournament fee paid by somebody who entered on the public form: they
	// may have no account at all, so the receipt goes to the address they gave.
	if regID.Valid {
		emailPublicEntrantReceipt(d, svc, regID.String, amount, forWhat)
	}
	if !studentID.Valid || studentID.String == "" {
		return
	}
	recipients := families.ParentAccountsOf(d, studentID.String)
	if len(recipients) == 0 {
		return
	}
	name := families.StudentDisplayName(d, studentID.String)

	var credits, balance float64
	d.QueryRow(
		`SELECT COALESCE(SUM(amount),0) FROM credit_transaction
		  WHERE payment_id = ? AND transaction_type = 'purchase'`, paymentID).Scan(&credits)
	if enrolmentID.Valid && enrolmentID.String != "" {
		d.QueryRow(
			`SELECT COALESCE(SUM(amount),0) FROM credit_transaction WHERE enrollment_id = ?`,
			enrolmentID.String).Scan(&balance)
	}

	amt := fees.FmtBaht(amount)
	en := "Your payment of " + amt + " was successful."
	th := "การชำระเงิน " + amt + " ของคุณสำเร็จแล้ว"
	if regID.Valid && forWhat != "" {
		// A tournament fee buys no credits, so what it was for is the news.
		en = "Your payment of " + amt + " for " + name + "'s entry to " + forWhat + " was successful."
		th = "ชำระค่าสมัคร " + forWhat + " ของ " + name + " จำนวน " + amt + " สำเร็จแล้ว"
	}
	if credits > 0 {
		en += " " + FmtCreditsShort(credits) + " credits have been added to " + name +
			"'s account. Current balance: " + FmtCreditsShort(balance) + " credits."
		th += " เพิ่ม " + FmtCreditsShort(credits) + " เครดิตให้ " + name +
			" แล้ว ยอดคงเหลือ " + FmtCreditsShort(balance) + " เครดิต"
	}
	/* The receipt, as the email's table. */
	same := func(v string) notify.Text { return notify.Text{EN: v, TH: v} }
	details := []notify.Detail{
		{Label: notify.Text{EN: "Student", TH: "นักเรียน"}, Value: same(name)},
	}
	if forWhat != "" {
		label := notify.Text{EN: "Course", TH: "คอร์ส"}
		if regID.Valid {
			label = notify.Text{EN: "Tournament", TH: "การแข่งขัน"}
		}
		details = append(details, notify.Detail{Label: label, Value: same(forWhat)})
	}
	details = append(details, notify.Detail{Label: notify.Text{EN: "Amount paid", TH: "ยอดชำระ"}, Value: same(amt)})
	if method != "" {
		details = append(details, notify.Detail{Label: notify.Text{EN: "Payment method", TH: "วิธีชำระเงิน"}, Value: same(paymentMethodLabel(method))})
	}
	if paidOn != "" {
		details = append(details, notify.Detail{Label: notify.Text{EN: "Date", TH: "วันที่"}, Value: same(FmtDay(paidOn))})
	}
	if reference != "" {
		details = append(details, notify.Detail{Label: notify.Text{EN: "Reference", TH: "เลขอ้างอิง"}, Value: same(reference)})
	}
	if credits > 0 {
		details = append(details,
			notify.Detail{Label: notify.Text{EN: "Credits added", TH: "เครดิตที่เพิ่ม"}, Value: same(FmtCreditsShort(credits))},
			notify.Detail{Label: notify.Text{EN: "New balance", TH: "ยอดคงเหลือ"}, Value: same(FmtCreditsShort(balance) + " credits")},
		)
	}
	svc.Send(recipients, notify.Message{
		Type:    notify.TypePayment,
		Title:   notify.Text{EN: "Payment successful", TH: "ชำระเงินสำเร็จ"},
		Body:    notify.Text{EN: en, TH: th},
		Details: details,
		EmailIntro: notify.Text{
			EN: "Thank you. We've received your payment for " + name + ". Here is your receipt.",
			TH: "ขอบคุณค่ะ เราได้รับการชำระเงินสำหรับ " + name + " แล้ว รายละเอียดอยู่ด้านล่าง",
		},
		Data:      map[string]any{"paymentId": paymentID, "studentId": studentID.String},
		DedupeKey: "payment_received:" + paymentID,
	})
}

// emailPublicEntrantReceipt confirms a paid tournament fee to the address a
// public entrant registered with. Parent and desk entries have no such address
// (their family is told through the inbox above), and an address that already
// belongs to one of the child's parents is skipped, so nobody gets it twice.
func emailPublicEntrantReceipt(d *sql.DB, svc *notify.Service, regID string, amount float64, tournament string) {
	var email, participant, source string
	var studentID sql.NullString
	if err := d.QueryRow(`
		SELECT COALESCE(contact_email, ''), participant_name, COALESCE(source, ''), student_id
		  FROM tournament_registration WHERE tournament_registration_id = ?`, regID).
		Scan(&email, &participant, &source, &studentID); err != nil {
		return
	}
	if source != "Public" || email == "" {
		return
	}
	if studentID.Valid {
		var n int
		d.QueryRow(`
			SELECT COUNT(*) FROM user_account u
			  JOIN parent p ON p.user_account_id = u.user_account_id
			  JOIN student_parent sp ON sp.parent_id = p.parent_id
			 WHERE sp.student_id = ? AND lower(trim(u.email)) = lower(trim(?))`,
			studentID.String, email).Scan(&n)
		if n > 0 {
			return
		}
	}
	// "Registration Confirmed", with the date, venue and amount paid: the one
	// email a family who paid gets, whether online or at the desk.
	entrymail.SendEntryNotice(d, svc, regID, entrymail.NoticeConfirmed, true)
}

// paymentMethodLabel reads the stored method ("BankTransfer") as people say it.
func paymentMethodLabel(method string) string {
	switch method {
	case "CreditCard":
		return "Credit card"
	case "BankTransfer":
		return "Bank transfer"
	}
	return method
}

// FmtDay writes a stored date as "27 Sep 2026", or leaves it be.
func FmtDay(iso string) string {
	if t, err := time.Parse("2006-01-02", iso[:min(len(iso), 10)]); err == nil {
		return t.Format("2 Jan 2006")
	}
	return iso
}
