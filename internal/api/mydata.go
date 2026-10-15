// A family's own data: a copy to download (PDPA s.30–31, access and
// portability) and a request to have it erased (s.33), which the office
// carries out with the people delete and records here (migration 0081).
package api

import (
	"database/sql"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Kusk24/jtrax-backend/internal/auth"
	"github.com/Kusk24/jtrax-backend/internal/httpx"
)

func mountMyData(mux *http.ServeMux, d *sql.DB) {
	// Each export reads every table a family touches, so it is budgeted.
	mux.HandleFunc("GET /api/v1/me/export", httpx.RateLimit(10, handleExport(d)))
	mux.HandleFunc("POST /api/v1/me/erasure-request", httpx.RateLimit(10, handleErasureRequest(d)))
	mux.HandleFunc("GET /api/v1/me/erasure-request", handleMyErasureRequest(d))
	mux.HandleFunc("GET /api/v1/data-requests", handleListDataRequests(d))
	mux.HandleFunc("POST /api/v1/data-requests/{id}/resolve", handleResolveDataRequest(d))
}

// rowsOf runs a query and returns its rows as column → value maps. The
// queries below name their columns, so a hash or a token can never leak in
// through a SELECT *.
func rowsOf(d *sql.DB, query string, args ...any) ([]map[string]any, error) {
	rows, err := d.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		row := map[string]any{}
		for i, c := range cols {
			if b, ok := vals[i].([]byte); ok {
				row[c] = string(b)
			} else {
				row[c] = vals[i]
			}
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// in builds "?,?,?" for a list of ids, and the args to go with it.
func in(ids []string) (string, []any) {
	if len(ids) == 0 {
		return "NULL", nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	return strings.TrimSuffix(strings.Repeat("?,", len(ids)), ","), args
}

// section is one part of an export: a name and the query that fills it.
type section struct {
	key   string
	query string
	args  []any
}

// exportFor gathers what the academy holds about the caller and, for a
// parent, their children. Scoped by the caller's own ids, never by a request
// parameter: the only family it can read is the caller's.
func exportFor(d *sql.DB, id *auth.Identity) (map[string]any, error) {
	out := map[string]any{
		"exported_at":    time.Now().UTC().Format(time.RFC3339),
		"notice_version": PrivacyNoticeVersion,
	}
	sections := []section{
		{"account", `SELECT email, role, display_name, language_preference, theme_preference
			FROM user_account WHERE user_account_id = ?`, []any{id.UserAccountID}},
		{"notifications", `SELECT type, title, body, created_at, read_at
			FROM notification WHERE user_account_id = ? ORDER BY created_at`, []any{id.UserAccountID}},
		{"consents", `SELECT purpose, notice_version, tournament_registration_id, given_at, withdrawn_at
			FROM consent_record WHERE user_account_id = ? ORDER BY given_at`, []any{id.UserAccountID}},
	}
	var students []string
	switch id.Role {
	case "Parent":
		sections = append(sections,
			section{"parent", `SELECT name FROM parent WHERE parent_id = ?`, []any{id.ParentID}},
			section{"contacts", `SELECT contact_type, value FROM parent_contact WHERE parent_id = ?`, []any{id.ParentID}},
			section{"line_messages", `SELECT m.direction, m.kind, m.body, m.sent_at
				FROM line_message m JOIN line_contact c ON c.line_user_id = m.line_user_id
				WHERE c.parent_id = ? ORDER BY m.sent_at`, []any{id.ParentID}},
		)
		kids, err := rowsOf(d, `SELECT student_id FROM student_parent WHERE parent_id = ?`, id.ParentID)
		if err != nil {
			return nil, err
		}
		for _, k := range kids {
			students = append(students, fmt.Sprint(k["student_id"]))
		}
	case "Student":
		students = []string{id.StudentID}
	}
	for _, s := range sections {
		rows, err := rowsOf(d, s.query, s.args...)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", s.key, err)
		}
		out[s.key] = rows
	}

	marks, args := in(students)
	perStudent := []struct{ key, query string }{
		{"students", `SELECT student_id, name, date_of_birth, current_level, fide_id, fide_rating,
			current_school, branch, streak_count, archived_at FROM student WHERE student_id IN (` + marks + `)`},
		{"enrolments", `SELECT e.student_id, c.name AS class, e.*
			FROM student_enrollment e LEFT JOIN class c ON c.class_id = e.class_id
			WHERE e.student_id IN (` + marks + `)`},
		{"attendance", `SELECT a.student_id, s.session_date, a.check_in_time, a.check_out_time
			FROM attendance a LEFT JOIN class_session s ON s.session_id = a.session_id
			WHERE a.student_id IN (` + marks + `) ORDER BY a.check_in_time`},
		{"payments", `SELECT student_id, student_name, parent_name, class_name, amount, discount_amount,
			final_amount, payment_method, status, payment_date, reference_number
			FROM payment WHERE student_id IN (` + marks + `) ORDER BY payment_date`},
		{"tournament_entries", `SELECT r.student_id, t.name AS tournament, r.participant_name, r.participant_name_th,
			r.nickname, r.participant_date_of_birth, r.participant_contact, r.contact_email, r.contact_phone,
			r.medical_notes, r.remarks, r.status, r.registered_at, r.fee_charged
			FROM tournament_registration r JOIN tournament t ON t.tournament_id = r.tournament_id
			WHERE r.student_id IN (` + marks + `) ORDER BY r.registered_at`},
		{"practice", `SELECT student_id, activity_date, minutes_practiced, puzzles_completed, points_earned
			FROM practice_activity WHERE student_id IN (` + marks + `) ORDER BY activity_date`},
		{"certificates", `SELECT student_id, course, certified_at, hours_at_certification
			FROM student_certificate WHERE student_id IN (` + marks + `)`},
		{"lichess", `SELECT student_id, username, linked_at FROM student_lichess WHERE student_id IN (` + marks + `)`},
	}
	for _, s := range perStudent {
		rows, err := rowsOf(d, s.query, args...)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", s.key, err)
		}
		out[s.key] = rows
	}
	return out, nil
}

// handleExport answers a parent or student with everything held about them,
// as a JSON file. Staff ask the office like anybody else: their accounts hold
// other people's data, which an export of "theirs" must not sweep up.
func handleExport(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := requireIdentity(d, w, r)
		if id == nil {
			return
		}
		if id.Role != "Parent" && id.Role != "Student" {
			httpx.Error(w, http.StatusForbidden, "ask the office for a copy of a staff account's data", nil)
			return
		}
		data, err := exportFor(d, id)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not export", err)
			return
		}
		w.Header().Set("Content-Disposition", `attachment; filename="jtrax-my-data.json"`)
		httpx.JSON(w, http.StatusOK, data)
	}
}

// handleErasureRequest records a parent's request to erase their family's
// data. One open request at a time: asking twice is the same request.
func handleErasureRequest(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := requireIdentity(d, w, r)
		if id == nil {
			return
		}
		if id.Role != "Parent" {
			httpx.Error(w, http.StatusForbidden, "a parent asks for their family", nil)
			return
		}
		var in struct {
			Note string `json:"note"`
		}
		if err := httpx.Decode(r, &in); err != nil {
			httpx.Error(w, http.StatusBadRequest, "invalid body", nil)
			return
		}
		in.Note = strings.TrimSpace(in.Note)
		if len([]rune(in.Note)) > 1000 {
			httpx.Error(w, http.StatusBadRequest, "that note is too long", nil)
			return
		}
		var existing string
		err := d.QueryRow(`SELECT data_request_id FROM data_request
			WHERE parent_id = ? AND kind = 'erasure' AND status = 'Open'`, id.ParentID).Scan(&existing)
		if err == nil {
			httpx.JSON(w, http.StatusOK, map[string]any{"data_request_id": existing, "status": "Open"})
			return
		}
		if err != sql.ErrNoRows {
			httpx.Error(w, http.StatusInternalServerError, "could not record the request", err)
			return
		}
		reqID := newID("dreq")
		if _, err := d.Exec(`INSERT INTO data_request
			(data_request_id, kind, user_account_id, parent_id, requested_by_name, note)
			VALUES (?, 'erasure', ?, ?, ?, ?)`,
			reqID, id.UserAccountID, id.ParentID, id.DisplayName, in.Note); err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not record the request", err)
			return
		}
		httpx.JSON(w, http.StatusCreated, map[string]any{"data_request_id": reqID, "status": "Open"})
	}
}

// handleMyErasureRequest says whether the caller's family has a request open,
// so the portal can show it rather than offer the button again.
func handleMyErasureRequest(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := requireIdentity(d, w, r)
		if id == nil {
			return
		}
		rows, err := rowsOf(d, `SELECT data_request_id, status, requested_at FROM data_request
			WHERE parent_id = ? AND kind = 'erasure' ORDER BY requested_at DESC LIMIT 1`, id.ParentID)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not read", err)
			return
		}
		if len(rows) == 0 {
			httpx.JSON(w, http.StatusOK, map[string]any{"status": ""})
			return
		}
		httpx.JSON(w, http.StatusOK, rows[0])
	}
}

// Staff see every request; only an admin closes one, as only an admin may
// delete the people it is about.
func handleListDataRequests(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if requireStaff(d, w, r) == nil {
			return
		}
		rows, err := rowsOf(d, `SELECT data_request_id, kind, status, parent_id, requested_by_name, note,
			requested_at, resolved_at, resolved_by, resolution_note
			FROM data_request ORDER BY status = 'Open' DESC, requested_at DESC`)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not read", err)
			return
		}
		httpx.JSON(w, http.StatusOK, rows)
	}
}

func handleResolveDataRequest(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := requireAdmin(d, w, r)
		if id == nil {
			return
		}
		var in struct {
			Status string `json:"status"`
			Note   string `json:"note"`
		}
		if err := httpx.Decode(r, &in); err != nil || (in.Status != "Done" && in.Status != "Declined") {
			httpx.Error(w, http.StatusBadRequest, "status must be Done or Declined", nil)
			return
		}
		in.Note = strings.TrimSpace(in.Note)
		/* A refusal has to say why — the family is owed a reason, and the
		   PDPC may ask for one. */
		if in.Status == "Declined" && in.Note == "" {
			httpx.Error(w, http.StatusBadRequest, "say why the request is declined", nil)
			return
		}
		res, err := d.Exec(`UPDATE data_request SET status = ?, resolution_note = ?, resolved_at = ?, resolved_by = ?
			WHERE data_request_id = ? AND status = 'Open'`,
			in.Status, in.Note, sqliteNow(), id.DisplayName, r.PathValue("id"))
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not update", err)
			return
		}
		if n, _ := res.RowsAffected(); n == 0 {
			httpx.Error(w, http.StatusNotFound, "no open request with that id", nil)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
