// Seed fills an empty database with development data matching the frontend
// mocks (Sandy + Penny/Uri, Ms. Serene, Beginner/Intermediate chess, the
// Wellington tournament) so the portals show familiar content on first run.
package db

import (
	"database/sql"
	"fmt"

	"github.com/Kusk24/jtrax-backend/internal/auth"
)

// Seed is a no-op when any user account already exists.
func Seed(d *sql.DB) error {
	var n int
	if err := d.QueryRow(`SELECT COUNT(*) FROM user_account`).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}

	// Same throwaway dev password for every seeded account.
	hash, err := auth.HashPassword("jtrax-dev-1234")
	if err != nil {
		return err
	}

	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	exec := func(q string, args ...any) error {
		_, err := tx.Exec(q, args...)
		if err != nil {
			return fmt.Errorf("%s: %w", q[:40], err)
		}
		return nil
	}

	type acct struct{ id, email, role, name string }
	accounts := []acct{
		{"usr_admin1", "admin@jca.ac.th", "Admin", "JCA Head Office"},
		{"usr_serene", "serene@jca.ac.th", "Teacher", "Ms. Serene"},
		{"usr_sandy", "sandy01234@gmail.com", "Parent", "Sandy Jones"},
		{"usr_penny", "penny@jca.ac.th", "Student", "Penny"},
		{"usr_uri", "uri@jca.ac.th", "Student", "Uri"},
	}
	for _, a := range accounts {
		if err := exec(`INSERT INTO user_account (user_account_id, email, password_hash, role, display_name)
			VALUES (?,?,?,?,?)`, a.id, a.email, hash, a.role, a.name); err != nil {
			return err
		}
	}

	steps := []struct {
		q    string
		args []any
	}{
		{`INSERT INTO admin (admin_id, user_account_id, name, phone, email) VALUES (?,?,?,?,?)`,
			[]any{"adm_1", "usr_admin1", "JCA Head Office", "02-123-4567", "admin@jca.ac.th"}},
		{`INSERT INTO teacher (teacher_id, user_account_id, name, phone, email) VALUES (?,?,?,?,?)`,
			[]any{"tch_serene", "usr_serene", "Ms. Serene", "081-111-2222", "serene@jca.ac.th"}},
		{`INSERT INTO parent (parent_id, user_account_id, name) VALUES (?,?,?)`,
			[]any{"par_sandy", "usr_sandy", "Sandy Jones"}},
		{`INSERT INTO parent_contact (parent_contact_id, parent_id, contact_type, value) VALUES (?,?,?,?)`,
			[]any{"pct_1", "par_sandy", "phone", "+66 12 345 6789"}},
		{`INSERT INTO parent_contact (parent_contact_id, parent_id, contact_type, value) VALUES (?,?,?,?)`,
			[]any{"pct_2", "par_sandy", "email", "sandy01234@gmail.com"}},
		{`INSERT INTO notification_preference (parent_id, check_in_alerts_enabled, credit_expiry_alerts_enabled, announcement_alerts_enabled)
			VALUES (?,?,?,?)`, []any{"par_sandy", 1, 1, 0}},

		{`INSERT INTO student (student_id, user_account_id, name, date_of_birth, current_level, streak_count) VALUES (?,?,?,?,?,?)`,
			[]any{"stu_penny", "usr_penny", "Penny", "2018-02-14", "Beginner", 12}},
		{`INSERT INTO student (student_id, user_account_id, name, date_of_birth, current_level, streak_count) VALUES (?,?,?,?,?,?)`,
			[]any{"stu_uri", "usr_uri", "Uri", "2016-06-02", "Intermediate", 5}},
		{`INSERT INTO student_parent (student_id, parent_id, relationship_type) VALUES (?,?,?)`,
			[]any{"stu_penny", "par_sandy", "mother"}},
		{`INSERT INTO student_parent (student_id, parent_id, relationship_type) VALUES (?,?,?)`,
			[]any{"stu_uri", "par_sandy", "mother"}},
		{`INSERT INTO practice_settings (student_id, daily_screen_time_limit_minutes) VALUES (?,?)`,
			[]any{"stu_penny", 90}},
		{`INSERT INTO practice_settings (student_id, daily_screen_time_limit_minutes) VALUES (?,?)`,
			[]any{"stu_uri", 90}},

		{`INSERT INTO class (class_id, name, description, class_type) VALUES (?,?,?,?)`,
			[]any{"cls_beg101", "Beginner Chess (Sec 101)", "Fundamentals for new players", "Group"}},
		{`INSERT INTO class (class_id, name, description, class_type) VALUES (?,?,?,?)`,
			[]any{"cls_int101", "Intermediate Chess (Sec 101)", "Tactics and openings", "Group"}},
		{`INSERT INTO credit_package (credit_package_id, class_id, credit_amount, standard_price, validity_days) VALUES (?,?,?,?,?)`,
			[]any{"pkg_beg20", "cls_beg101", 20, 12000, 120}},
		{`INSERT INTO credit_package (credit_package_id, class_id, credit_amount, standard_price, validity_days) VALUES (?,?,?,?,?)`,
			[]any{"pkg_int20", "cls_int101", 20, 14000, 120}},

		{`INSERT INTO student_enrollment (enrollment_id, student_id, class_id, enrolled_date, status) VALUES (?,?,?,?,?)`,
			[]any{"enr_penny", "stu_penny", "cls_beg101", "2026-01-10", "Active"}},
		{`INSERT INTO student_enrollment (enrollment_id, student_id, class_id, enrolled_date, status) VALUES (?,?,?,?,?)`,
			[]any{"enr_uri", "stu_uri", "cls_int101", "2026-01-10", "Active"}},

		{`INSERT INTO class_session (session_id, class_id, session_date, start_time, end_time, duration_hours, session_status) VALUES (?,?,?,?,?,?,?)`,
			[]any{"ses_b1", "cls_beg101", "2026-05-05", "14:00", "15:00", 1, "Completed"}},
		{`INSERT INTO class_session (session_id, class_id, session_date, start_time, end_time, duration_hours, session_status) VALUES (?,?,?,?,?,?,?)`,
			[]any{"ses_b2", "cls_beg101", "2026-05-10", "14:00", "15:00", 1, "Completed"}},
		{`INSERT INTO class_session (session_id, class_id, session_date, start_time, end_time, duration_hours, session_status) VALUES (?,?,?,?,?,?,?)`,
			[]any{"ses_b3", "cls_beg101", "2026-05-20", "14:00", "15:00", 1, "Scheduled"}},
		{`INSERT INTO class_session (session_id, class_id, session_date, start_time, end_time, duration_hours, session_status) VALUES (?,?,?,?,?,?,?)`,
			[]any{"ses_i1", "cls_int101", "2026-05-10", "09:00", "10:00", 1, "Completed"}},

		{`INSERT INTO attendance (attendance_id, student_id, session_id, check_in_time, check_out_time) VALUES (?,?,?,?,?)`,
			[]any{"att_1", "stu_penny", "ses_b1", "2026-05-05T13:55:00", "2026-05-05T15:02:00"}},
		{`INSERT INTO attendance (attendance_id, student_id, session_id, check_in_time, check_out_time) VALUES (?,?,?,?,?)`,
			[]any{"att_2", "stu_penny", "ses_b2", "2026-05-10T13:58:00", "2026-05-10T15:00:00"}},
		{`INSERT INTO attendance (attendance_id, student_id, session_id, check_in_time, check_out_time) VALUES (?,?,?,?,?)`,
			[]any{"att_3", "stu_uri", "ses_i1", "2026-05-10T09:00:00", "2026-05-10T10:01:00"}},

		{`INSERT INTO payment (payment_id, student_id, enrollment_id, credit_package_id, amount, discount_amount, final_amount, payment_method, status, payment_date, reference_number)
			VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
			[]any{"pay_1", "stu_penny", "enr_penny", "pkg_beg20", 12000, 0, 12000, "PromptPay", "Paid", "2026-01-10", "PP-20260110-001"}},
		{`INSERT INTO payment (payment_id, student_id, enrollment_id, credit_package_id, amount, discount_amount, final_amount, payment_method, status, payment_date, reference_number)
			VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
			[]any{"pay_2", "stu_uri", "enr_uri", "pkg_int20", 14000, 1000, 13000, "BankTransfer", "Paid", "2026-01-10", "BT-20260110-002"}},
		{`INSERT INTO credit_transaction (credit_transaction_id, enrollment_id, transaction_type, amount, expiry_date, transaction_date, payment_id)
			VALUES (?,?,?,?,?,?,?)`,
			[]any{"ctx_1", "enr_penny", "purchase", 20, "2026-05-20", "2026-01-10", "pay_1"}},
		{`INSERT INTO credit_transaction (credit_transaction_id, enrollment_id, transaction_type, amount, expiry_date, transaction_date, payment_id)
			VALUES (?,?,?,?,?,?,?)`,
			[]any{"ctx_2", "enr_uri", "purchase", 20, "2026-06-20", "2026-01-10", "pay_2"}},
		{`INSERT INTO credit_transaction (credit_transaction_id, enrollment_id, transaction_type, amount, transaction_date, attendance_id)
			VALUES (?,?,?,?,?,?)`,
			[]any{"ctx_3", "enr_penny", "consumption", -1, "2026-05-05", "att_1"}},

		{`INSERT INTO announcement (announcement_id, title, body, author_user_account_id, posted_at, has_attachment) VALUES (?,?,?,?,?,?)`,
			[]any{"ann_1", "Class Moved to Room 1B",
				"Penny's Beginner Chess class on Wed will be held in Room 1B instead of 1A due to maintenance.",
				"usr_serene", "2026-05-10T08:10:00", 1}},
		{`INSERT INTO announcement (announcement_id, title, body, author_user_account_id, posted_at, has_attachment) VALUES (?,?,?,?,?,?)`,
			[]any{"ann_2", "Public Holiday Closure",
				"The Bangkok branch will be closed on May 12 for a public holiday. All classes that day are rescheduled.",
				"usr_admin1", "2026-05-09T17:00:00", 1}},

		{`INSERT INTO tournament (tournament_id, name, tournament_status, start_date, end_date, venue_name, venue_address, organizer_name, registration_deadline, early_bird_fee, regular_fee, max_participants)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
			[]any{"trn_wellington", "Wellington College Chess Championship 2026", "Upcoming",
				"2026-06-07", "2026-06-07", "Wellington College International School Bangkok",
				"Bangkok", "Wellington College", "2026-06-05", 250, 300, 128}},
		{`INSERT INTO tournament_category (tournament_category_id, tournament_id, name) VALUES (?,?,?)`,
			[]any{"tcat_u8", "trn_wellington", "Under 8"}},
		{`INSERT INTO tournament_category (tournament_category_id, tournament_id, name) VALUES (?,?,?)`,
			[]any{"tcat_u10", "trn_wellington", "Under 10"}},

		{`INSERT INTO practice_activity (activity_id, student_id, activity_date, minutes_practiced, puzzles_completed, points_earned, streak_count)
			VALUES (?,?,?,?,?,?,?)`,
			[]any{"act_1", "stu_penny", "2026-05-10", 32, 3, 30, 12}},
		{`INSERT INTO practice_activity (activity_id, student_id, activity_date, minutes_practiced, puzzles_completed, points_earned, streak_count)
			VALUES (?,?,?,?,?,?,?)`,
			[]any{"act_2", "stu_uri", "2026-05-10", 18, 2, 20, 5}},

		{`INSERT INTO system_configuration (config_key, config_value) VALUES (?,?)`,
			[]any{"academy_name", "JCA Chess Academy"}},
	}
	for _, s := range steps {
		if err := exec(s.q, s.args...); err != nil {
			return err
		}
	}
	return tx.Commit()
}
