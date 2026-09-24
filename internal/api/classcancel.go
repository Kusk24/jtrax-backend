// Calling a class off, and telling the families it was called off.
//
// The console used to cancel a class by deleting its attendance rows and then
// the session, one request at a time. That refunded the credits (each
// attendance delete runs refundAttendance) but told nobody: a parent found out
// when they arrived at a locked door. This endpoint does the same three things
// in one transaction, then notifies the parents of every child who was due at
// that class.
//
// Only a class that has not happened yet is announced. Cancelling a session
// from last week is the office tidying the timetable, and a "class cancelled"
// message about the past would only confuse.
package api

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/Kusk24/jtrax-backend/internal/httpx"
	"github.com/Kusk24/jtrax-backend/internal/notify"
)

func mountClassCancel(mux *http.ServeMux, d *sql.DB, svc *notify.Service) {
	mux.HandleFunc("POST /api/v1/class-sessions/{id}/cancel", handleCancelClass(d, svc))
}

// handleCancelClass refunds, deletes and announces one session. Staff only:
// a teacher can run a class, but calling one off for every family is the
// office's decision.
func handleCancelClass(d *sql.DB, svc *notify.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := requireIdentity(d, w, r)
		if id == nil {
			return
		}
		if !isStaff(id.Role) {
			httpx.Error(w, http.StatusForbidden, "only admin or reception may cancel a class", nil)
			return
		}
		sessionID := r.PathValue("id")

		tx, err := d.Begin()
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not cancel the class", err)
			return
		}
		defer tx.Rollback()

		var classID, className, day, start, end string
		err = tx.QueryRow(`
			SELECT s.class_id, COALESCE(c.name, ''), s.session_date, s.start_time, s.end_time
			  FROM class_session s
			  LEFT JOIN class c ON c.class_id = s.class_id
			 WHERE s.session_id = ?`, sessionID).Scan(&classID, &className, &day, &start, &end)
		if errors.Is(err, sql.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, "no such class", nil)
			return
		}
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not cancel the class", err)
			return
		}

		// Who was due: everyone enrolled in the class, and anyone already on
		// this session's roster (a make-up student is on the roster without
		// being enrolled). Read before the attendance goes.
		students, err := studentsDueAt(tx, classID, sessionID)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not cancel the class", err)
			return
		}

		// Refund, then delete: the same order the console used, for the same
		// reason. attendance.session_id has no cascade.
		rows, err := tx.Query(`SELECT attendance_id FROM attendance WHERE session_id = ?`, sessionID)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not cancel the class", err)
			return
		}
		var attIDs []string
		for rows.Next() {
			var a string
			if err := rows.Scan(&a); err != nil {
				rows.Close()
				httpx.Error(w, http.StatusInternalServerError, "could not cancel the class", err)
				return
			}
			attIDs = append(attIDs, a)
		}
		rows.Close()
		for _, a := range attIDs {
			if err := refundAttendance(tx, a); err != nil {
				httpx.Error(w, http.StatusInternalServerError, "could not refund the class", err)
				return
			}
		}
		if _, err := tx.Exec(`DELETE FROM attendance WHERE session_id = ?`, sessionID); err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not cancel the class", err)
			return
		}
		if _, err := tx.Exec(`DELETE FROM class_session WHERE session_id = ?`, sessionID); err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not cancel the class", err)
			return
		}
		if err := tx.Commit(); err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not cancel the class", err)
			return
		}

		notified := 0
		if day >= today() {
			notified = announceCancellation(d, svc, sessionID, students, className, day, start, end)
		}
		httpx.JSON(w, http.StatusOK, map[string]any{
			"cancelled":         true,
			"refunded":          len(attIDs),
			"students_notified": notified,
		})
	}
}

// studentsDueAt is every student expected at a session, each once.
func studentsDueAt(tx *sql.Tx, classID, sessionID string) ([]string, error) {
	rows, err := tx.Query(`
		SELECT student_id FROM student_enrollment WHERE class_id = ? AND status = 'Active'
		UNION
		SELECT student_id FROM attendance WHERE session_id = ?`, classID, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// announceCancellation tells each child's parents, once per session. It runs
// after the commit and never fails the cancellation: the class is off whether
// or not a message went out, and the desk can still ring.
func announceCancellation(d *sql.DB, svc *notify.Service, sessionID string, students []string, className, day, start, end string) int {
	en, th := sessionDay(day)
	if className == "" {
		className = "Chess class"
	}
	sent := 0
	for _, sid := range students {
		recipients := parentAccountsOf(d, sid)
		if len(recipients) == 0 {
			continue
		}
		name := studentDisplayName(d, sid)
		if err := svc.Send(recipients, notify.Message{
			Type:  notify.TypeClassCancelled,
			Title: notify.Text{EN: "Class cancelled", TH: "ยกเลิกคลาสเรียน"},
			Body: notify.Text{
				EN: className + " on " + en + ", " + start + " – " + end + ", is cancelled. " +
					name + " does not need to come, and no credit is used for it.",
				TH: "คลาส " + className + " วัน" + th + " เวลา " + start + " – " + end + " ถูกยกเลิก " +
					name + " ไม่ต้องมาเรียน และไม่มีการหักเครดิตสำหรับคลาสนี้",
			},
			Data:      map[string]any{"studentId": sid, "sessionId": sessionID, "date": day},
			DedupeKey: "class_cancelled:" + sessionID + ":" + sid,
		}); err == nil {
			sent++
		}
	}
	return sent
}

var (
	enWeekdays = [...]string{"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"}
	enMonths   = [...]string{"Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"}
	thWeekdays = [...]string{"อาทิตย์", "จันทร์", "อังคาร", "พุธ", "พฤหัสบดี", "ศุกร์", "เสาร์"}
	thMonths   = [...]string{"ม.ค.", "ก.พ.", "มี.ค.", "เม.ย.", "พ.ค.", "มิ.ย.", "ก.ค.", "ส.ค.", "ก.ย.", "ต.ค.", "พ.ย.", "ธ.ค."}
)

// sessionDay writes a class date the way each language says it: "Thu 24 Sep
// 2026" and "พฤหัสบดีที่ 24 ก.ย. 2569", with the Buddhist-era year Thai
// readers expect. A date that does not parse is returned as it is.
func sessionDay(iso string) (en, th string) {
	t, err := time.Parse("2006-01-02", iso)
	if err != nil {
		return iso, iso
	}
	day := t.Format("2")
	en = enWeekdays[t.Weekday()] + " " + day + " " + enMonths[t.Month()-1] + " " + t.Format("2006")
	th = thWeekdays[t.Weekday()] + "ที่ " + day + " " + thMonths[t.Month()-1] + " " + strconv.Itoa(t.Year()+543)
	return en, th
}
