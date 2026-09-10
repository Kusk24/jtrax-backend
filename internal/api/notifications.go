// Notification endpoints: the in-app inbox, per-user channel preferences, push
// subscription registration, and the one manual trigger (credit-expiry) that a
// person sets off rather than a schedule.
//
// Every read is scoped by the caller's own user_account_id in the WHERE clause,
// never filtered afterwards — a parent must not be able to fetch another
// family's inbox. Push subscriptions are treated as per-user secrets: a
// caller's own are the only ones they can see or delete.
package api

import (
	"database/sql"
	"math"
	"net/http"
	"strconv"

	"github.com/Kusk24/jtrax-backend/internal/auth"
	"github.com/Kusk24/jtrax-backend/internal/httpx"
	"github.com/Kusk24/jtrax-backend/internal/notify"
)

// mountNotifications wires the inbox, settings, subscriptions and the manual
// credit-expiry trigger. The service is shared with the registry hooks that
// fire on check-in and on a new announcement.
func mountNotifications(mux *http.ServeMux, d *sql.DB, svc *notify.Service) {
	mux.HandleFunc("GET /api/v1/notifications", handleListNotifications(d))
	mux.HandleFunc("POST /api/v1/notifications/{id}/read", handleMarkRead(d))
	mux.HandleFunc("POST /api/v1/notifications/read-all", handleMarkAllRead(d))

	mux.HandleFunc("GET /api/v1/notification-settings", handleGetSettings(d))
	mux.HandleFunc("PUT /api/v1/notification-settings", handlePutSettings(d))

	mux.HandleFunc("POST /api/v1/push-subscriptions", handleRegisterPush(d))
	mux.HandleFunc("DELETE /api/v1/push-subscriptions", handleUnregisterPush(d))

	// Manual, permission-gated: only Admin / Receptionist may set it off.
	mux.HandleFunc("POST /api/v1/notifications/credit-expiry", handleCreditExpiry(d, svc))
}

// ---- inbox ---------------------------------------------------------------

func handleListNotifications(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := requireIdentity(d, w, r)
		if id == nil {
			return
		}
		// Scoped to the caller in the query itself.
		rows, err := d.Query(
			`SELECT notification_id, type, title, body, COALESCE(data,''), created_at, COALESCE(read_at,'')
			 FROM notification WHERE user_account_id = ?
			 ORDER BY created_at DESC, notification_id DESC LIMIT 100`, id.UserAccountID)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not load notifications", err)
			return
		}
		defer rows.Close()

		list := []map[string]any{}
		unread := 0
		for rows.Next() {
			var nid, typ, title, body, data, createdAt, readAt string
			if err := rows.Scan(&nid, &typ, &title, &body, &data, &createdAt, &readAt); err != nil {
				httpx.Error(w, http.StatusInternalServerError, "could not read notifications", err)
				return
			}
			if readAt == "" {
				unread++
			}
			list = append(list, map[string]any{
				"notification_id": nid, "type": typ, "title": title, "body": body,
				"data": data, "created_at": createdAt, "read_at": nullable(readAt),
			})
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"notifications": list, "unread": unread})
	}
}

func handleMarkRead(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := requireIdentity(d, w, r)
		if id == nil {
			return
		}
		// The user_account_id predicate is the authorization: a caller can only
		// mark their own rows, and touching someone else's is a silent no-op.
		res, err := d.Exec(
			`UPDATE notification SET read_at = datetime('now')
			 WHERE notification_id = ? AND user_account_id = ? AND read_at IS NULL`,
			r.PathValue("id"), id.UserAccountID)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not mark read", err)
			return
		}
		n, _ := res.RowsAffected()
		httpx.JSON(w, http.StatusOK, map[string]any{"updated": n})
	}
}

func handleMarkAllRead(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := requireIdentity(d, w, r)
		if id == nil {
			return
		}
		res, err := d.Exec(
			`UPDATE notification SET read_at = datetime('now')
			 WHERE user_account_id = ? AND read_at IS NULL`, id.UserAccountID)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not mark read", err)
			return
		}
		n, _ := res.RowsAffected()
		httpx.JSON(w, http.StatusOK, map[string]any{"updated": n})
	}
}

// ---- preferences ---------------------------------------------------------

// handleGetSettings returns the caller's stored overrides. An absent (type,
// channel) means the default, which is on — the client fills the grid from
// that rule, so the response is only the exceptions.
func handleGetSettings(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := requireIdentity(d, w, r)
		if id == nil {
			return
		}
		rows, err := d.Query(
			`SELECT type, channel, enabled FROM notification_setting WHERE user_account_id = ?`,
			id.UserAccountID)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not load settings", err)
			return
		}
		defer rows.Close()
		settings := []map[string]any{}
		for rows.Next() {
			var typ, channel string
			var enabled int
			if err := rows.Scan(&typ, &channel, &enabled); err != nil {
				httpx.Error(w, http.StatusInternalServerError, "could not read settings", err)
				return
			}
			settings = append(settings, map[string]any{"type": typ, "channel": channel, "enabled": enabled != 0})
		}
		httpx.JSON(w, http.StatusOK, map[string]any{
			"settings": settings,
			"types":    []string{notify.TypeCheckIn, notify.TypeCheckOut, notify.TypeCreditExpiry, notify.TypeAnnouncement},
			"channels": notify.Channels,
		})
	}
}

type settingInput struct {
	Type    string `json:"type"`
	Channel string `json:"channel"`
	Enabled bool   `json:"enabled"`
}

// handlePutSettings upserts one override for the caller. The in-app row for a
// type is that person's master toggle — most alert types are in-app only, so
// "Check-in Alerts: off" has to mean off, and the opt-in types (low credit)
// are turned on the same way.
func handlePutSettings(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := requireIdentity(d, w, r)
		if id == nil {
			return
		}
		var in settingInput
		if err := httpx.Decode(r, &in); err != nil {
			httpx.Error(w, http.StatusBadRequest, "invalid body", err)
			return
		}
		if !validType(in.Type) || !validChannel(in.Channel) {
			httpx.Error(w, http.StatusBadRequest, "unknown type or channel", nil)
			return
		}
		enabled := 0
		if in.Enabled {
			enabled = 1
		}
		if _, err := d.Exec(
			`INSERT INTO notification_setting (user_account_id, type, channel, enabled)
			 VALUES (?,?,?,?)
			 ON CONFLICT(user_account_id, type, channel) DO UPDATE SET enabled = excluded.enabled`,
			id.UserAccountID, in.Type, in.Channel, enabled); err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not save setting", err)
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"saved": true})
	}
}

// ---- push subscriptions --------------------------------------------------

type pushInput struct {
	Channel   string `json:"channel"` // "webpush" or "mobile"
	Endpoint  string `json:"endpoint"`
	P256dh    string `json:"p256dh"`
	Auth      string `json:"auth"`
	UserAgent string `json:"user_agent"`
}

func handleRegisterPush(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := requireIdentity(d, w, r)
		if id == nil {
			return
		}
		var in pushInput
		if err := httpx.Decode(r, &in); err != nil {
			httpx.Error(w, http.StatusBadRequest, "invalid body", err)
			return
		}
		if (in.Channel != notify.ChannelWebPush && in.Channel != notify.ChannelMobile) || in.Endpoint == "" {
			httpx.Error(w, http.StatusBadRequest, "channel must be webpush or mobile, with an endpoint", nil)
			return
		}
		// endpoint is UNIQUE: the same browser re-registering updates its owner
		// and last_seen_at rather than piling up rows. Re-binding to the current
		// caller also stops a stale row pointing at a previous account.
		if _, err := d.Exec(
			`INSERT INTO push_subscription
			   (push_subscription_id, user_account_id, channel, endpoint, p256dh, auth, user_agent)
			 VALUES (?,?,?,?,?,?,?)
			 ON CONFLICT(endpoint) DO UPDATE SET
			   user_account_id = excluded.user_account_id,
			   channel = excluded.channel, p256dh = excluded.p256dh, auth = excluded.auth,
			   user_agent = excluded.user_agent, last_seen_at = datetime('now'), failed_at = NULL`,
			newID("psb"), id.UserAccountID, in.Channel, in.Endpoint, nullable(in.P256dh), nullable(in.Auth), nullable(in.UserAgent)); err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not register", err)
			return
		}
		httpx.JSON(w, http.StatusCreated, map[string]any{"registered": true})
	}
}

func handleUnregisterPush(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := requireIdentity(d, w, r)
		if id == nil {
			return
		}
		var in pushInput
		if err := httpx.Decode(r, &in); err != nil {
			httpx.Error(w, http.StatusBadRequest, "invalid body", err)
			return
		}
		// Scoped to the caller: you can only remove your own endpoint.
		if _, err := d.Exec(
			`DELETE FROM push_subscription WHERE endpoint = ? AND user_account_id = ?`,
			in.Endpoint, id.UserAccountID); err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not unregister", err)
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"unregistered": true})
	}
}

// ---- manual credit-expiry trigger ---------------------------------------

// handleCreditExpiry notifies the parents of students whose credits expire
// within `days` (default 14). It is manual and permission-gated to staff — the
// academy wants a person to decide when this goes out, not a schedule.
func handleCreditExpiry(d *sql.DB, svc *notify.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := requireIdentity(d, w, r)
		if id == nil {
			return
		}
		if !isStaff(id.Role) {
			httpx.Error(w, http.StatusForbidden, "only admin or reception may send this", nil)
			return
		}
		days := 14
		if q := r.URL.Query().Get("days"); q != "" {
			if v, err := strconv.Atoi(q); err == nil && v > 0 && v <= 365 {
				days = v
			}
		}

		// Enrollments with credit expiring in the window, and the student behind
		// them. Grouped so a student with several expiring lots is notified once.
		rows, err := d.Query(
			`SELECT DISTINCT e.student_id, COALESCE(s.name,'')
			   FROM credit_transaction ct
			   JOIN student_enrollment e ON e.enrollment_id = ct.enrollment_id
			   JOIN student s ON s.student_id = e.student_id
			  WHERE ct.expiry_date IS NOT NULL
			    AND date(ct.expiry_date) >= date('now')
			    AND date(ct.expiry_date) <= date('now', '+' || ? || ' days')`, days)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not find expiring credits", err)
			return
		}
		defer rows.Close()

		type target struct{ studentID, studentName string }
		var targets []target
		for rows.Next() {
			var t target
			if err := rows.Scan(&t.studentID, &t.studentName); err != nil {
				httpx.Error(w, http.StatusInternalServerError, "could not read expiring credits", err)
				return
			}
			targets = append(targets, t)
		}
		rows.Close()

		sent := 0
		for _, t := range targets {
			recipients := parentAccountsOf(d, t.studentID)
			if len(recipients) == 0 {
				continue
			}
			name := t.studentName
			if name == "" {
				name = "your child"
			}
			err := svc.Send(recipients, notify.Message{
				Type:  notify.TypeCreditExpiry,
				Title: notify.Text{EN: "Class credits expiring soon", TH: "เครดิตเรียนใกล้หมดอายุ"},
				Body: notify.Text{
					EN: name + "'s class credits expire within " + strconv.Itoa(days) + " days. Please top up to avoid a gap.",
					TH: "เครดิตเรียนของ" + name + " จะหมดอายุภายใน " + strconv.Itoa(days) + " วัน กรุณาเติมเครดิตเพื่อไม่ให้ขาดช่วง",
				},
				Data:      map[string]any{"studentId": t.studentID, "days": days},
				DedupeKey: "credit_expiry:" + t.studentID + ":" + today(),
			})
			if err != nil {
				httpx.Error(w, http.StatusInternalServerError, "could not send", err)
				return
			}
			sent++
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"students_notified": sent, "within_days": days})
	}
}

// ---- triggers on generic writes -----------------------------------------

// attachNotificationHooks wires the check-in and announcement notifications
// onto the generic resources that already handle those writes, so a teacher's
// existing check-in PATCH and an admin's existing announcement POST each grow a
// notification without a bespoke endpoint.
func attachNotificationHooks(resources []*Resource, d *sql.DB, svc *notify.Service) {
	for _, rs := range resources {
		switch rs.Name {
		case "attendance":
			rs.AfterCommit = attendanceHook(svc)
		case "announcements":
			rs.AfterCommit = announcementHook(d, svc)
		case "payments":
			rs.AfterCommit = paymentHook(svc)
		}
	}
}

// attendanceHook notifies a student's parents when they are checked in or out.
// The dedupe key is the attendance row plus the direction, so the same PATCH
// arriving twice — or a later edit to the row — notifies once each way.
func attendanceHook(svc *notify.Service) func(*sql.DB, *auth.Identity, map[string]any, bool) {
	return func(d *sql.DB, _ *auth.Identity, row map[string]any, _ bool) {
		attID := rowStr(row, "attendance_id")
		studentID := rowStr(row, "student_id")
		if attID == "" || studentID == "" {
			return
		}
		recipients := parentAccountsOf(d, studentID)
		if len(recipients) == 0 {
			return
		}
		name := studentDisplayName(d, studentID)

		if rowStr(row, "check_out_time") != "" {
			sendCheckoutNotifications(d, svc, recipients, attID, studentID, name)
			return
		}
		if rowStr(row, "check_in_time") != "" {
			svc.Send(recipients, notify.Message{
				Type:      notify.TypeCheckIn,
				Title:     notify.Text{EN: "Checked in", TH: "เช็คอินแล้ว"},
				Body:      notify.Text{EN: name + " has arrived for class.", TH: name + " มาถึงคลาสแล้ว"},
				Data:      map[string]any{"studentId": studentID, "attendanceId": attID},
				DedupeKey: "checkin:" + attID,
			})
		}
	}
}

// announcementHook fans a newly posted announcement out to every student and
// parent. Staff-authored text is free-form and single-language, so the same
// title/body goes to everyone regardless of their language preference.
func announcementHook(_ *sql.DB, svc *notify.Service) func(*sql.DB, *auth.Identity, map[string]any, bool) {
	return func(d *sql.DB, _ *auth.Identity, row map[string]any, created bool) {
		if !created {
			return // editing an announcement does not re-notify the school
		}
		annID := rowStr(row, "announcement_id")
		title := rowStr(row, "title")
		body := rowStr(row, "body")
		if annID == "" {
			return
		}
		recipients := allStudentAndParentAccounts(d)
		if len(recipients) == 0 {
			return
		}
		svc.Send(recipients, notify.Message{
			Type:      notify.TypeAnnouncement,
			Title:     notify.Text{EN: title, TH: title},
			Body:      notify.Text{EN: body, TH: body},
			Data:      map[string]any{"announcementId": annID},
			DedupeKey: "announcement:" + annID,
		})
	}
}

// ---- shared helpers ------------------------------------------------------

// parentAccountsOf returns the user_account_ids of a student's linked parents.
func parentAccountsOf(d *sql.DB, studentID string) []string {
	rows, err := d.Query(
		`SELECT p.user_account_id FROM student_parent sp
		   JOIN parent p ON p.parent_id = sp.parent_id
		  WHERE sp.student_id = ? AND p.user_account_id IS NOT NULL`, studentID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var uid string
		if err := rows.Scan(&uid); err == nil && uid != "" {
			ids = append(ids, uid)
		}
	}
	return ids
}

func allStudentAndParentAccounts(d *sql.DB) []string {
	rows, err := d.Query(
		`SELECT user_account_id FROM user_account WHERE role IN ('Parent','Student')`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var uid string
		if err := rows.Scan(&uid); err == nil && uid != "" {
			ids = append(ids, uid)
		}
	}
	return ids
}

func studentDisplayName(d *sql.DB, studentID string) string {
	var name sql.NullString
	d.QueryRow(`SELECT name FROM student WHERE student_id = ?`, studentID).Scan(&name)
	if name.Valid && name.String != "" {
		return name.String
	}
	return "Your child"
}

func rowStr(row map[string]any, key string) string {
	if v, ok := row[key]; ok && v != nil {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func validType(t string) bool {
	switch t {
	case notify.TypeCheckIn, notify.TypeCheckOut, notify.TypeCreditDeducted, notify.TypeLowCredit,
		notify.TypeCreditExpiry, notify.TypeAnnouncement, notify.TypePayment:
		return true
	}
	return false
}

func validChannel(c string) bool {
	for _, ch := range notify.Channels {
		if ch == c {
			return true
		}
	}
	return false
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// ---- credit deduction on check-out ---------------------------------------

// sendCheckoutNotifications tells the parents what the class actually cost.
//
// Credits are deducted at check-out, so the check-out notice is the deduction
// notice: class time, credits used, credits left. A check-out that charged
// nothing — a session with no readable length, or a student with no enrolment
// to charge — falls back to the plain "has left class", because inventing a
// zero-credit receipt would read as a free class rather than a gap.
//
// If the balance this leaves is at or under the academy's low-credit line, a
// second, separate notification nudges the parent to top up. That one is
// opt-in (see notify.DefaultEnabled) and held to once a day per student.
func sendCheckoutNotifications(d *sql.DB, svc *notify.Service, recipients []string, attID, studentID, name string) {
	var used, remaining float64
	var enrolmentID, start, end string
	err := d.QueryRow(
		`SELECT -ct.amount, ct.enrollment_id, cs.start_time, cs.end_time
		   FROM credit_transaction ct
		   JOIN attendance a ON a.attendance_id = ct.attendance_id
		   JOIN class_session cs ON cs.session_id = a.session_id
		  WHERE ct.attendance_id = ? AND ct.transaction_type = 'consumption'`,
		attID).Scan(&used, &enrolmentID, &start, &end)
	if err != nil {
		svc.Send(recipients, notify.Message{
			Type:      notify.TypeCheckOut,
			Title:     notify.Text{EN: "Checked out", TH: "เช็คเอาท์แล้ว"},
			Body:      notify.Text{EN: name + " has left class.", TH: name + " ออกจากคลาสแล้ว"},
			Data:      map[string]any{"studentId": studentID, "attendanceId": attID},
			DedupeKey: "checkout:" + attID,
		})
		return
	}
	d.QueryRow(
		`SELECT COALESCE(SUM(amount),0) FROM credit_transaction WHERE enrollment_id = ?`,
		enrolmentID).Scan(&remaining)

	when := start + " – " + end
	svc.Send(recipients, notify.Message{
		Type:  notify.TypeCreditDeducted,
		Title: notify.Text{EN: "Class credit deducted", TH: "หักเครดิตคลาสเรียนแล้ว"},
		Body: notify.Text{
			EN: name + " has completed their chess class. Class time: " + when +
				". Credit used: " + fmtCreditsShort(used) + ". Remaining credit: " + fmtCreditsShort(remaining) + ".",
			TH: name + " เรียนจบคลาสแล้ว เวลาเรียน " + when +
				" ใช้ไป " + fmtCreditsShort(used) + " เครดิต คงเหลือ " + fmtCreditsShort(remaining) + " เครดิต",
		},
		Data:      map[string]any{"studentId": studentID, "attendanceId": attID},
		DedupeKey: "credit_deducted:" + attID,
	})

	if remaining <= lowCreditLine(d) {
		svc.Send(recipients, notify.Message{
			Type:  notify.TypeLowCredit,
			Title: notify.Text{EN: "Low credit balance", TH: "เครดิตเหลือน้อย"},
			Body: notify.Text{
				EN: name + " has " + fmtCreditsShort(remaining) + " credits remaining. " +
					"Consider purchasing additional credits to continue their classes without interruption.",
				TH: name + " เหลือเครดิต " + fmtCreditsShort(remaining) + " เครดิต " +
					"กรุณาเติมเครดิตเพื่อให้เรียนต่อได้ไม่ขาดช่วง",
			},
			Data:      map[string]any{"studentId": studentID},
			DedupeKey: "low_credit:" + studentID + ":" + today(),
		})
	}
}

// lowCreditLine is the academy's own threshold, the same
// `credit_rule_low_credit` the console's Settings screen edits. Default 3,
// matching the console's.
func lowCreditLine(d *sql.DB) float64 {
	var raw string
	if err := d.QueryRow(
		`SELECT config_value FROM system_configuration WHERE config_key = 'credit_rule_low_credit'`,
	).Scan(&raw); err == nil {
		if v, err := strconv.ParseFloat(raw, 64); err == nil && v >= 0 {
			return v
		}
	}
	return 3
}

// fmtCreditsShort writes a credit count the way a person would: whole numbers
// bare, fractions to at most two places — 13.499999999999998 is the right
// number and the wrong thing to put in a sentence.
func fmtCreditsShort(v float64) string {
	r := math.Round(v*100) / 100
	if r == math.Trunc(r) {
		return strconv.FormatInt(int64(r), 10)
	}
	return strconv.FormatFloat(r, 'f', -1, 64)
}

// ---- payment received ----------------------------------------------------

// paymentHook notifies the guardian when a payment lands as Paid — whether
// the desk recorded it Paid outright, marked a pending one Paid later, or the
// Stripe webhook settled it (which calls notifyPaymentPaid itself, since it
// writes SQL directly rather than through the resource). The dedupe key is the
// payment, so edits to a settled row never re-notify.
func paymentHook(svc *notify.Service) func(*sql.DB, *auth.Identity, map[string]any, bool) {
	return func(d *sql.DB, _ *auth.Identity, row map[string]any, _ bool) {
		if rowStr(row, "status") != "Paid" {
			return
		}
		notifyPaymentPaid(d, svc, rowStr(row, "payment_id"))
	}
}

// notifyPaymentPaid sends the PDF's "Payment Successful" message: the amount,
// the credits it bought, and the balance they leave. In-app and email both —
// a receipt is the one notification the academy wants in writing.
func notifyPaymentPaid(d *sql.DB, svc *notify.Service, paymentID string) {
	if paymentID == "" {
		return
	}
	var studentID, enrolmentID sql.NullString
	var amount float64
	if err := d.QueryRow(
		`SELECT student_id, enrollment_id, final_amount FROM payment WHERE payment_id = ?`,
		paymentID).Scan(&studentID, &enrolmentID, &amount); err != nil {
		return
	}
	if !studentID.Valid || studentID.String == "" {
		return
	}
	recipients := parentAccountsOf(d, studentID.String)
	if len(recipients) == 0 {
		return
	}
	name := studentDisplayName(d, studentID.String)

	var credits, balance float64
	d.QueryRow(
		`SELECT COALESCE(SUM(amount),0) FROM credit_transaction
		  WHERE payment_id = ? AND transaction_type = 'purchase'`, paymentID).Scan(&credits)
	if enrolmentID.Valid && enrolmentID.String != "" {
		d.QueryRow(
			`SELECT COALESCE(SUM(amount),0) FROM credit_transaction WHERE enrollment_id = ?`,
			enrolmentID.String).Scan(&balance)
	}

	amt := fmtBaht(amount)
	en := "Your payment of " + amt + " was successful."
	th := "การชำระเงิน " + amt + " ของคุณสำเร็จแล้ว"
	if credits > 0 {
		en += " " + fmtCreditsShort(credits) + " credits have been added to " + name +
			"'s account. Current balance: " + fmtCreditsShort(balance) + " credits."
		th += " เพิ่ม " + fmtCreditsShort(credits) + " เครดิตให้ " + name +
			" แล้ว ยอดคงเหลือ " + fmtCreditsShort(balance) + " เครดิต"
	}
	svc.Send(recipients, notify.Message{
		Type:      notify.TypePayment,
		Title:     notify.Text{EN: "Payment successful", TH: "ชำระเงินสำเร็จ"},
		Body:      notify.Text{EN: en, TH: th},
		Data:      map[string]any{"paymentId": paymentID, "studentId": studentID.String},
		DedupeKey: "payment_received:" + paymentID,
	})
}

// fmtBaht writes an amount with a thousands separator and the ISO code —
// "12,000 THB". Whole satang are the norm at the till, so decimals only
// appear when the amount has them.
func fmtBaht(v float64) string {
	whole := int64(math.Trunc(math.Abs(v)))
	frac := math.Round((math.Abs(v)-float64(whole))*100) / 100
	digits := strconv.FormatInt(whole, 10)
	var out []byte
	for i, c := range []byte(digits) {
		if i > 0 && (len(digits)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, c)
	}
	text := string(out)
	if frac > 0 {
		text += strconv.FormatFloat(frac, 'f', 2, 64)[1:]
	}
	if v < 0 {
		text = "-" + text
	}
	return text + " THB"
}
