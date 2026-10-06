// Credit reminders, sent by themselves.
//
// The academy's rules (Settings → Academy Rules) say when a balance is low
// (`credit_rule_low_credit`) and how soon before expiry credits count as
// expiring (`credit_rule_expiring_days`). Parents choose, in their own
// notification settings, whether they want each reminder; svc.Send honours
// that, and the school's own switch for the type, on every send.
//
//   - Low credit: as soon as a check-out leaves a child at or under the line,
//     and from the hourly sweep for any other way a balance got there (a
//     class's automatic start, a desk adjustment). Once until the family next
//     tops up, not at every class.
//   - Expiring credits: from the hourly sweep, once per expiry date, as soon
//     as it falls inside the window.
//
// Both are in-app only — the bell in the parent portal, never an email or a
// push — and each goes once, never daily.
//
// The sweep sends only in the academy's daytime, so nobody is woken by it.
// The manual triggers in notifications.go still work and share the same
// lookups below.
package api

import (
	"context"
	"database/sql"
	"log"
	"sort"
	"strconv"
	"time"

	"github.com/Kusk24/jtrax-backend/internal/academytime"
	"github.com/Kusk24/jtrax-backend/internal/notify"
)

// reminderEvery is how often the sweep looks; reminderHours is when it may send.
const reminderEvery = time.Hour

const (
	reminderFromHour = 9
	reminderToHour   = 20
)

type lowTarget struct {
	studentID, studentName string
	balance                float64
}

// lowCreditTargets is every student with an active course at or under the
// line — or just `onlyStudent`, when given — lowest balance first. A balance is
// counted as the console counts it: a course's own credits plus the child's
// credits that sit in no course; a child in two courses is low if either is.
func lowCreditTargets(d *sql.DB, onlyStudent string) ([]lowTarget, float64, error) {
	line := lowCreditLine(d)
	rows, err := d.Query(`
		SELECT e.student_id, COALESCE(s.name, ''),
		       COALESCE((SELECT SUM(ct.amount) FROM credit_transaction ct
		                  WHERE ct.enrollment_id = e.enrollment_id), 0)
		     + COALESCE((SELECT SUM(ct.amount) FROM credit_transaction ct
		                  WHERE ct.enrollment_id IS NULL AND ct.student_id = e.student_id), 0)
		  FROM student_enrollment e
		  JOIN student s ON s.student_id = e.student_id
		 WHERE e.status = 'Active' AND (? = '' OR e.student_id = ?)`, onlyStudent, onlyStudent)
	if err != nil {
		return nil, line, err
	}
	defer rows.Close()
	lowest := map[string]*lowTarget{}
	var order []string
	for rows.Next() {
		var t lowTarget
		if err := rows.Scan(&t.studentID, &t.studentName, &t.balance); err != nil {
			return nil, line, err
		}
		if t.balance > line {
			continue
		}
		if prev, seen := lowest[t.studentID]; seen {
			if t.balance < prev.balance {
				prev.balance = t.balance
			}
			continue
		}
		lowest[t.studentID] = &t
		order = append(order, t.studentID)
	}
	if err := rows.Err(); err != nil {
		return nil, line, err
	}
	targets := make([]lowTarget, 0, len(order))
	for _, sid := range order {
		targets = append(targets, *lowest[sid])
	}
	// Lowest balance first: those are the families to call before a class.
	sort.SliceStable(targets, func(i, j int) bool {
		if targets[i].balance != targets[j].balance {
			return targets[i].balance < targets[j].balance
		}
		return targets[i].studentName < targets[j].studentName
	})
	return targets, line, nil
}

type expiryTarget struct{ studentID, studentName, expires string }

// expiringTargets is every student with credits expiring between today and
// `days` from now, each once, about the soonest.
func expiringTargets(d *sql.DB, days int) ([]expiryTarget, error) {
	rows, err := d.Query(
		`SELECT e.student_id, COALESCE(s.name,''), MIN(date(ct.expiry_date))
		   FROM credit_transaction ct
		   JOIN student_enrollment e ON e.enrollment_id = ct.enrollment_id
		   JOIN student s ON s.student_id = e.student_id
		  WHERE ct.expiry_date IS NOT NULL
		    AND date(ct.expiry_date) >= ?
		    AND date(ct.expiry_date) <= date(?, '+' || ? || ' days')
		  GROUP BY e.student_id, s.name`, today(), today(), days)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var targets []expiryTarget
	for rows.Next() {
		var t expiryTarget
		if err := rows.Scan(&t.studentID, &t.studentName, &t.expires); err != nil {
			return nil, err
		}
		targets = append(targets, t)
	}
	return targets, rows.Err()
}

func lowCreditMessage(t lowTarget, dedupe string) notify.Message {
	name := t.studentName
	if name == "" {
		name = "your child"
	}
	return notify.Message{
		Type:  notify.TypeLowCredit,
		Title: notify.Text{EN: "Low credit balance", TH: "เครดิตเหลือน้อย"},
		Body: notify.Text{
			EN: name + " has " + fmtCreditsShort(t.balance) + " credits remaining. " +
				"Please top up to continue their classes without interruption.",
			TH: name + " เหลือเครดิต " + fmtCreditsShort(t.balance) + " เครดิต " +
				"กรุณาเติมเครดิตเพื่อให้เรียนต่อได้ไม่ขาดช่วง",
		},
		Data:      map[string]any{"studentId": t.studentID},
		DedupeKey: dedupe,
		InAppOnly: true,
	}
}

func expiryMessage(t expiryTarget, days int, dedupe string) notify.Message {
	name := t.studentName
	if name == "" {
		name = "your child"
	}
	return notify.Message{
		Type:  notify.TypeCreditExpiry,
		Title: notify.Text{EN: "Class credits expiring soon", TH: "เครดิตเรียนใกล้หมดอายุ"},
		Body: notify.Text{
			EN: name + "'s class credits expire within " + strconv.Itoa(days) + " days. Please top up to avoid a gap.",
			TH: "เครดิตเรียนของ" + name + " จะหมดอายุภายใน " + strconv.Itoa(days) + " วัน กรุณาเติมเครดิตเพื่อไม่ให้ขาดช่วง",
		},
		Data:      map[string]any{"studentId": t.studentID, "days": days},
		DedupeKey: dedupe,
		InAppOnly: true,
	}
}

// expiringDaysRule is how many days before expiry credits count as expiring —
// the console's `credit_rule_expiring_days`, default 7.
func expiringDaysRule(d *sql.DB) int {
	var raw string
	if err := d.QueryRow(
		`SELECT config_value FROM system_configuration WHERE config_key = 'credit_rule_expiring_days'`,
	).Scan(&raw); err == nil {
		if v, err := strconv.Atoi(raw); err == nil && v > 0 && v <= 365 {
			return v
		}
	}
	return 7
}

// lastTopUp names the child's most recent credit purchase or grant, so a
// low-credit reminder goes once per top-up rather than once per class.
func lastTopUp(d *sql.DB, studentID string) string {
	var id string
	err := d.QueryRow(`
		SELECT credit_transaction_id FROM credit_transaction
		 WHERE amount > 0
		   AND (student_id = ? OR enrollment_id IN
		        (SELECT enrollment_id FROM student_enrollment WHERE student_id = ?))
		 ORDER BY transaction_date DESC, rowid DESC LIMIT 1`, studentID, studentID).Scan(&id)
	if err != nil {
		return "none"
	}
	return id
}

// remindLowCredit tells one child's parents their balance is low, if it is.
// Returns whether the child was low (sent, or already told since the last
// top-up).
func remindLowCredit(d *sql.DB, svc *notify.Service, t lowTarget) bool {
	recipients := parentAccountsOf(d, t.studentID)
	if len(recipients) == 0 {
		return false
	}
	key := "low_credit:auto:" + t.studentID + ":" + lastTopUp(d, t.studentID)
	if err := svc.Send(recipients, lowCreditMessage(t, key)); err != nil {
		log.Printf("credit reminders: low credit for %s: %v", t.studentID, err)
	}
	return true
}

// remindIfLowCredit checks one child straight after a check-out.
func remindIfLowCredit(d *sql.DB, svc *notify.Service, studentID string) {
	targets, _, err := lowCreditTargets(d, studentID)
	if err != nil {
		log.Printf("credit reminders: balance of %s: %v", studentID, err)
		return
	}
	for _, t := range targets {
		remindLowCredit(d, svc, t)
	}
}

// RunCreditReminders sends the reminders that are due at `now`. Outside the
// academy's daytime it sends nothing; anything due waits for the morning.
func RunCreditReminders(d *sql.DB, svc *notify.Service, now time.Time) {
	if h := now.In(academytime.Location()).Hour(); h < reminderFromHour || h >= reminderToHour {
		return
	}
	if low, _, err := lowCreditTargets(d, ""); err != nil {
		log.Printf("credit reminders: low credit: %v", err)
	} else {
		for _, t := range low {
			remindLowCredit(d, svc, t)
		}
	}
	days := expiringDaysRule(d)
	expiring, err := expiringTargets(d, days)
	if err != nil {
		log.Printf("credit reminders: expiring: %v", err)
		return
	}
	for _, t := range expiring {
		recipients := parentAccountsOf(d, t.studentID)
		if len(recipients) == 0 {
			continue
		}
		key := "credit_expiry:auto:" + t.studentID + ":" + t.expires
		if err := svc.Send(recipients, expiryMessage(t, days, key)); err != nil {
			log.Printf("credit reminders: expiry for %s: %v", t.studentID, err)
		}
	}
}

// StartCreditReminders runs RunCreditReminders now and then every hour until
// ctx ends. Started by the server, not by NewHandler, so tests control when
// it runs.
func StartCreditReminders(ctx context.Context, d *sql.DB, svc *notify.Service) {
	go func() {
		RunCreditReminders(d, svc, time.Now())
		ticker := time.NewTicker(reminderEvery)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				RunCreditReminders(d, svc, now)
			}
		}
	}()
}
