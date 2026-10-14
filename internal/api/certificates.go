// Certificates by course.
//
// JCA Starters, JCA Juniors and JCA NXT each take 50 hours of class (the
// academy's certificate_hours) and a passed assessment. Hours are the credits
// charged for classes — one credit is one hour — added up across every class
// of the course, private and group alike, never across courses. So hours and
// credits used can never disagree: an early leave rounded at check-out moves
// both the same way. Only class charges count (consumption tied to an
// attendance); a top-up, a desk adjustment or a transfer is not time in
// class, and a visit nothing was charged for adds no hours. They are read from
// the ledger on every request, so a corrected check-in moves them at once.
//
// Reaching the hours certifies nobody: the office marks a student certified
// after the assessment, once per course.
package api

import (
	"database/sql"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/Kusk24/jtrax-backend/internal/academytime"
	"github.com/Kusk24/jtrax-backend/internal/httpx"
)

// certificateCourses are the courses a certificate can be for, in order.
var certificateCourses = []string{"starters", "juniors", "nxt"}

// defaultCertificateHours is used until the academy saves its own.
const defaultCertificateHours = 50

func requiredHours(d *sql.DB) float64 {
	var v string
	d.QueryRow(`SELECT config_value FROM system_configuration WHERE config_key = 'certificate_hours'`).Scan(&v)
	if n, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil && n > 0 {
		return n
	}
	return defaultCertificateHours
}

// courseHours is the hours attended in each certificate course: the credits
// its classes charged this student.
func courseHours(d *sql.DB, studentID string) (map[string]float64, error) {
	rows, err := d.Query(`
		SELECT c.certificate_course, -SUM(ct.amount)
		  FROM credit_transaction ct
		  /* A charge keeps its student and class when its course is deleted
		     from the child (the entry is detached, not removed): counted by
		     those, the hours stay. */
		  LEFT JOIN student_enrollment e ON e.enrollment_id = ct.enrollment_id
		  JOIN class c ON c.class_id = COALESCE(e.class_id, ct.class_id)
		 WHERE COALESCE(e.student_id, ct.student_id) = ? AND ct.transaction_type = 'consumption'
		   AND ct.attendance_id IS NOT NULL
		   AND COALESCE(c.certificate_course, '') <> ''
		 GROUP BY c.certificate_course`, studentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]float64{}
	for rows.Next() {
		var course string
		var hours float64
		if err := rows.Scan(&course, &hours); err != nil {
			return nil, err
		}
		if hours > 0 {
			out[course] = hours
		}
	}
	return out, rows.Err()
}

type courseCertificate struct {
	Course        string   `json:"course"`
	Hours         float64  `json:"hours"`
	Status        string   `json:"status"` // "in_progress" or "certified"
	CertificateID string   `json:"certificate_id,omitempty"`
	CertifiedAt   string   `json:"certified_at,omitempty"`
	HoursAtIssue  *float64 `json:"hours_at_certification,omitempty"`
}

// studentCertificates is each course the student is in, has hours in, or is
// certified for.
func studentCertificates(d *sql.DB, studentID string) ([]courseCertificate, error) {
	hours, err := courseHours(d, studentID)
	if err != nil {
		return nil, err
	}
	issued := map[string]courseCertificate{}
	rows, err := d.Query(`SELECT course, certificate_id, certified_at, hours_at_certification
	                        FROM student_certificate WHERE student_id = ?`, studentID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var c courseCertificate
		var h float64
		if err := rows.Scan(&c.Course, &c.CertificateID, &c.CertifiedAt, &h); err != nil {
			rows.Close()
			return nil, err
		}
		c.HoursAtIssue = &h
		issued[c.Course] = c
	}
	rows.Close()
	enrolled := map[string]bool{}
	// Only courses the child is in now: a course left or deleted is not one
	// they are working toward (a certificate already earned is kept below).
	rows, err = d.Query(`SELECT DISTINCT c.certificate_course FROM student_enrollment e
	                       JOIN class c ON c.class_id = e.class_id
	                      WHERE e.student_id = ? AND COALESCE(c.certificate_course, '') <> ''
	                        AND e.deleted_date IS NULL AND COALESCE(e.status, 'Active') = 'Active'`, studentID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var course string
		if rows.Scan(&course) == nil {
			enrolled[course] = true
		}
	}
	rows.Close()

	out := []courseCertificate{}
	for _, course := range certificateCourses {
		c, done := issued[course]
		// A course in progress shows while the child is enrolled in it; one
		// already certified always shows, being a record of what they earned.
		if !done && !enrolled[course] {
			continue
		}
		c.Course = course
		c.Hours = math.Round(hours[course]*100) / 100
		c.Status = "in_progress"
		if done {
			c.Status = "certified"
		}
		out = append(out, c)
	}
	return out, nil
}

func handleStudentCertificates(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := requireIdentity(d, w, r)
		if id == nil {
			return
		}
		studentID := r.PathValue("id")
		if !canSeeStudent(d, id, studentID) {
			httpx.Error(w, http.StatusNotFound, "no such student", nil)
			return
		}
		list, err := studentCertificates(d, studentID)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not read certificates", err)
			return
		}
		var name string
		d.QueryRow(`SELECT name FROM student WHERE student_id = ?`, studentID).Scan(&name)
		httpx.JSON(w, http.StatusOK, map[string]any{
			"student_name": name, "required_hours": requiredHours(d), "courses": list,
		})
	}
}

// handleCertify marks a student certified for a course: staff only, once
// they have the hours, once per course.
func handleCertify(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		caller := requireStaff(d, w, r)
		if caller == nil {
			return
		}
		studentID := r.PathValue("id")
		var in struct {
			Course string `json:"course"`
		}
		if err := httpx.Decode(r, &in); err != nil || !slices.Contains(certificateCourses, in.Course) {
			httpx.Error(w, http.StatusBadRequest, "course must be starters, juniors or nxt", err)
			return
		}
		var n int
		d.QueryRow(`SELECT COUNT(*) FROM student WHERE student_id = ?`, studentID).Scan(&n)
		if n == 0 {
			httpx.Error(w, http.StatusNotFound, "no such student", nil)
			return
		}
		hours, err := courseHours(d, studentID)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not read hours", err)
			return
		}
		need := requiredHours(d)
		if hours[in.Course]+1e-9 < need {
			httpx.Error(w, http.StatusConflict, "not enough hours yet for this certificate", nil)
			return
		}
		certID := newID("cert")
		_, err = d.Exec(`INSERT INTO student_certificate
		                   (certificate_id, student_id, course, certified_at, certified_by, hours_at_certification)
		                 VALUES (?,?,?,?,?,?)`,
			certID, studentID, in.Course, academytime.Today(), caller.UserAccountID,
			math.Round(hours[in.Course]*100)/100)
		if err != nil {
			if strings.Contains(err.Error(), "UNIQUE") {
				httpx.Error(w, http.StatusConflict, "already certified for this course", nil)
				return
			}
			httpx.Error(w, http.StatusInternalServerError, "could not certify", err)
			return
		}
		list, err := studentCertificates(d, studentID)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not read certificates", err)
			return
		}
		httpx.JSON(w, http.StatusCreated, map[string]any{"required_hours": need, "courses": list})
	}
}

func mountCertificates(mux *http.ServeMux, d *sql.DB) {
	mux.HandleFunc("GET /api/v1/students/{id}/certificates", handleStudentCertificates(d))
	mux.HandleFunc("POST /api/v1/students/{id}/certificates", handleCertify(d))
}
