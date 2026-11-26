// Notification hooks for attendance, checkout and announcements.
package api

import (
	"database/sql"
	"math"
	"strconv"

	"github.com/Kusk24/jtrax-backend/internal/academytime"
	"github.com/Kusk24/jtrax-backend/internal/auth"
	"github.com/Kusk24/jtrax-backend/internal/notify"
)

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
			// The class is paid for now; if it left them low, say so (once).
			remindIfLowCredit(d, svc, studentID)
			return
		}
		if rowStr(row, "check_in_time") != "" {
			svc.Send(recipients, notify.Message{
				Type:      notify.TypeCheckIn,
				Title:     notify.Text{EN: "Checked in", TH: "เช็คอินแล้ว"},
				Body:      notify.Text{EN: name + " has arrived for class.", TH: name + " มาถึงคลาสแล้ว"},
				Data:      map[string]any{"studentId": studentID, "attendanceId": attID},
				DedupeKey: "checkin:" + attID,
				InAppOnly: true,
			})
		}
	}
}

// announcementHook fans a newly posted announcement out to the parents it is
// addressed to — every parent, or the ones resolved from its audience (see
// announcements.go). Staff-authored text is free-form and single-language, so
// the same title/body goes to everyone regardless of their language preference.
func announcementHook(_ *sql.DB, svc *notify.Service) func(*sql.DB, *auth.Identity, map[string]any, bool) {
	return func(d *sql.DB, _ *auth.Identity, row map[string]any, _ bool) {
		/* Told once, when parents can first see it: now for one already
		   showing, else by the schedule (announcementwindow.go). An edit that
		   moves the start to now is picked up the same way. */
		annID := rowStr(row, "announcement_id")
		if annID == "" {
			return
		}
		RunAnnouncementScheduleFor(d, svc, annID, academyMinute())
	}
}

// ---- shared helpers ------------------------------------------------------

// parentAccountsOf returns the user_account_ids of a student's linked parents.
func parentAccountsOf(d *sql.DB, studentID string) []string {
	rows, err := d.Query(
		`SELECT p.user_account_id FROM student_parent sp
		   JOIN parent p ON p.parent_id = sp.parent_id
		  WHERE sp.student_id = ? AND p.user_account_id IS NOT NULL
		    AND COALESCE(p.archived_at, '') = ''`, studentID)
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

// parentNamesOf is the display half of parentAccountsOf: the same join, but
// returning names for a person to read in the send preview rather than
// account ids to deliver to.
func parentNamesOf(d *sql.DB, studentID string) []string {
	rows, err := d.Query(
		`SELECT p.name FROM student_parent sp
		   JOIN parent p ON p.parent_id = sp.parent_id
		  WHERE sp.student_id = ?`, studentID)
	if err != nil {
		return []string{}
	}
	defer rows.Close()
	names := []string{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err == nil && name != "" {
			names = append(names, name)
		}
	}
	return names
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

// allTypes is the notification catalogue, in the order the settings screens
// list it.
var allTypes = []string{
	notify.TypeCheckIn, notify.TypeCheckOut, notify.TypeCreditDeducted, notify.TypeLowCredit,
	notify.TypeCreditExpiry, notify.TypeAnnouncement, notify.TypePayment, notify.TypeClassCancelled,
}

func validType(t string) bool {
	switch t {
	case notify.TypeCheckIn, notify.TypeCheckOut, notify.TypeCreditDeducted, notify.TypeLowCredit,
		notify.TypeCreditExpiry, notify.TypeAnnouncement, notify.TypePayment, notify.TypeClassCancelled:
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
// It no longer nudges a family whose balance ran low. The academy wants a
// person to decide when that goes out, so it is a staff button now, beside the
// expiry reminder: see handleLowCredit.
func sendCheckoutNotifications(d *sql.DB, svc *notify.Service, recipients []string, attID, studentID, name string) {
	var used, remaining float64
	var enrolmentID, start, end, day string
	var checkIn, checkOut sql.NullString
	err := d.QueryRow(
		`SELECT -ct.amount, ct.enrollment_id, cs.start_time, cs.end_time, cs.session_date,
		        a.check_in_time, a.check_out_time
		   FROM credit_transaction ct
		   JOIN attendance a ON a.attendance_id = ct.attendance_id
		   JOIN class_session cs ON cs.session_id = a.session_id
		  WHERE ct.attendance_id = ? AND ct.transaction_type = 'consumption'`,
		attID).Scan(&used, &enrolmentID, &start, &end, &day, &checkIn, &checkOut)
	if err != nil {
		svc.Send(recipients, notify.Message{
			Type:      notify.TypeCheckOut,
			Title:     notify.Text{EN: "Checked out", TH: "เช็คเอาท์แล้ว"},
			Body:      notify.Text{EN: name + " has left class.", TH: name + " ออกจากคลาสแล้ว"},
			Data:      map[string]any{"studentId": studentID, "attendanceId": attID},
			DedupeKey: "checkout:" + attID,
			InAppOnly: true,
		})
		return
	}
	d.QueryRow(
		`SELECT COALESCE(SUM(amount),0) FROM credit_transaction WHERE enrollment_id = ?`,
		enrolmentID).Scan(&remaining)

	when := start + " – " + end
	stayEN, stayTH := attendedPart(day, start, end, checkIn.String, checkOut.String)
	svc.Send(recipients, notify.Message{
		Type:  notify.TypeCreditDeducted,
		Title: notify.Text{EN: "Class credit deducted", TH: "หักเครดิตคลาสเรียนแล้ว"},
		Body: notify.Text{
			EN: name + " has completed their chess class. Class time: " + when + "." + stayEN +
				" Credit used: " + fmtCreditsShort(used) + ". Remaining credit: " + fmtCreditsShort(remaining) + ".",
			TH: name + " เรียนจบคลาสแล้ว เวลาเรียน " + when + stayTH +
				" ใช้ไป " + fmtCreditsShort(used) + " เครดิต คงเหลือ " + fmtCreditsShort(remaining) + " เครดิต",
		},
		Data:      map[string]any{"studentId": studentID, "attendanceId": attID},
		DedupeKey: "credit_deducted:" + attID,
		InAppOnly: true,
	})
}

// attendedPart says when the child actually was in class, when that is what
// the credits were charged for: arriving late or leaving early charges only
// the part attended (attendedHours), so a "1 credit" for a two-hour class
// needs its times beside it. Empty when they were there start to end.
func attendedPart(day, start, end, checkIn, checkOut string) (en, th string) {
	begin, okB := academytime.At(day, start)
	finish, okE := academytime.At(day, end)
	if !okB || !okE {
		return "", ""
	}
	zone := academytime.Location()
	in, okIn := academytime.Moment(checkIn)
	late := okIn && in.After(begin)
	out, okOut := academytime.Moment(checkOut)
	early := okOut && out.Before(finish)
	switch {
	case late && early:
		arrived, left := in.In(zone).Format("15:04"), out.In(zone).Format("15:04")
		return " Attended: " + arrived + " – " + left + " (arrived late, left early).",
			" เข้าเรียนจริง " + arrived + " – " + left + " (มาสายและกลับก่อนเวลา)"
	case late:
		arrived := in.In(zone).Format("15:04")
		return " Attended: " + arrived + " – " + end + " (arrived late).",
			" เข้าเรียนจริง " + arrived + " – " + end + " (มาสาย)"
	case early:
		left := out.In(zone).Format("15:04")
		return " Left early at " + left + ".", " กลับก่อนเวลาเมื่อ " + left
	}
	return "", ""
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
