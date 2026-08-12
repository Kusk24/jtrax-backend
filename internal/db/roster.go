// The roster is the family data the admin console used to hard-code: ten
// students, each with a parent, their class, credits and contact details.
// Importing it turns those fixtures into real accounts that sign in to the
// parent and student portals.
//
// Unlike Seed, which refuses to touch a database that has any account, this is
// keyed on email and class name and can be re-run against a live database.
package db

import (
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Kusk24/jtrax-backend/internal/auth"
)

//go:embed roster.json
var rosterJSON []byte

type RosterClass struct {
	Name        string `json:"name"`
	ClassType   string `json:"class_type"`
	Description string `json:"description"`
}

type RosterParent struct {
	Name   string `json:"name"`
	Email  string `json:"email"`
	Phone  string `json:"phone"`
	LineID string `json:"line_id"`
}

type RosterStudent struct {
	Name         string  `json:"name"`
	Email        string  `json:"email"`
	DateOfBirth  string  `json:"date_of_birth"`
	Level        string  `json:"level"`
	Relation     string  `json:"relation"`
	Class        string  `json:"class"`
	EnrolledDate string  `json:"enrolled_date"`
	Credits      float64 `json:"credits"`
	// Expiry is an offset, not a date: the console's literal dates have all
	// gone past, and a roster that imports as "everyone expired" demonstrates
	// nothing. The offsets reproduce the status each student was written with.
	ExpiresInDays       int `json:"expires_in_days"`
	LastAttendedDaysAgo int `json:"last_attended_days_ago"`
	Streak              int `json:"streak"`
}

type RosterFamily struct {
	Parent   RosterParent    `json:"parent"`
	Students []RosterStudent `json:"students"`
}

type Roster struct {
	Classes  []RosterClass  `json:"classes"`
	Families []RosterFamily `json:"families"`
}

// LoadRoster parses the embedded fixture.
func LoadRoster() (*Roster, error) {
	var r Roster
	if err := json.Unmarshal(rosterJSON, &r); err != nil {
		return nil, fmt.Errorf("roster.json: %w", err)
	}
	if len(r.Families) == 0 {
		return nil, errors.New("roster.json has no families")
	}
	return &r, nil
}

// ImportRoster writes the roster, using password for every account it creates.
// It reports the emails it wrote. Re-running is safe: rows are matched on
// email and class name, so a second run updates rather than duplicates.
//
// today is passed in rather than read from the clock so the dates it derives
// are reproducible in tests.
func ImportRoster(d *sql.DB, r *Roster, password string, today time.Time) ([]string, error) {
	if password == "" {
		return nil, errors.New("roster: password is required")
	}
	// One hash for every account, as Seed does. They all share the password, so
	// separate salts would hide nothing that the shared password does not
	// already give away — and PBKDF2 per account is slow on a small instance.
	hash, err := auth.HashPassword(password)
	if err != nil {
		return nil, err
	}

	classIDs := map[string]string{}
	for _, c := range r.Classes {
		id, err := ensureClass(d, c)
		if err != nil {
			return nil, err
		}
		classIDs[c.Name] = id
	}

	written := []string{}
	for _, f := range r.Families {
		parentID, err := ensureParent(d, f.Parent, hash)
		if err != nil {
			return nil, err
		}
		written = append(written, f.Parent.Email)

		for _, st := range f.Students {
			classID, ok := classIDs[st.Class]
			if !ok {
				return nil, fmt.Errorf("%s: no class named %q in the roster", st.Name, st.Class)
			}
			if err := ensureStudent(d, st, parentID, classID, hash, today); err != nil {
				return nil, err
			}
			written = append(written, st.Email)
		}
	}
	return written, nil
}

// slugID builds a stable id from an email local part or a name, so re-running
// the import lands on the same rows without needing a lookup table.
func slugID(prefix, source string) string {
	s := strings.ToLower(source)
	if at := strings.Index(s, "@"); at >= 0 {
		s = s[:at]
	}
	var b strings.Builder
	b.WriteString(prefix)
	b.WriteString("_")
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}

func ensureClass(d *sql.DB, c RosterClass) (string, error) {
	var id string
	err := d.QueryRow(`SELECT class_id FROM class WHERE name = ?`, c.Name).Scan(&id)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		id = slugID("cls", c.Name)
		_, err := d.Exec(`INSERT INTO class (class_id, name, description, class_type) VALUES (?,?,?,?)`,
			id, c.Name, c.Description, c.ClassType)
		if err != nil {
			return "", fmt.Errorf("create class %s: %w", c.Name, err)
		}
	case err != nil:
		return "", fmt.Errorf("look up class %s: %w", c.Name, err)
	}
	return id, nil
}

// ensureAccount creates or updates the user_account for email and returns its
// id, reusing the id of an account that already exists so a roster email that
// was seeded earlier is adopted rather than duplicated.
func ensureAccount(d *sql.DB, email, role, name, hash, idPrefix string) (string, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	var id string
	err := d.QueryRow(`SELECT user_account_id FROM user_account WHERE email = ?`, email).Scan(&id)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		id = slugID(idPrefix, email)
		if _, err := d.Exec(`INSERT INTO user_account (user_account_id, email, password_hash, role, display_name)
			VALUES (?,?,?,?,?)`, id, email, hash, role, name); err != nil {
			return "", fmt.Errorf("create account %s: %w", email, err)
		}
	case err != nil:
		return "", fmt.Errorf("look up account %s: %w", email, err)
	default:
		if _, err := d.Exec(`UPDATE user_account SET password_hash = ?, role = ?, display_name = ?
			WHERE user_account_id = ?`, hash, role, name, id); err != nil {
			return "", fmt.Errorf("update account %s: %w", email, err)
		}
		if _, err := d.Exec(`DELETE FROM auth_session WHERE user_account_id = ?`, id); err != nil {
			return "", fmt.Errorf("clear sessions for %s: %w", email, err)
		}
	}
	return id, nil
}

func ensureParent(d *sql.DB, p RosterParent, hash string) (string, error) {
	accountID, err := ensureAccount(d, p.Email, "Parent", p.Name, hash, "usr")
	if err != nil {
		return "", err
	}

	var parentID string
	err = d.QueryRow(`SELECT parent_id FROM parent WHERE user_account_id = ?`, accountID).Scan(&parentID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		parentID = slugID("par", p.Email)
		if _, err := d.Exec(`INSERT INTO parent (parent_id, user_account_id, name) VALUES (?,?,?)`,
			parentID, accountID, p.Name); err != nil {
			return "", fmt.Errorf("create parent %s: %w", p.Email, err)
		}
	case err != nil:
		return "", fmt.Errorf("look up parent %s: %w", p.Email, err)
	default:
		if _, err := d.Exec(`UPDATE parent SET name = ? WHERE parent_id = ?`, p.Name, parentID); err != nil {
			return "", fmt.Errorf("update parent %s: %w", p.Email, err)
		}
	}

	// The office's contact details, which are separate from the login address:
	// the console shows both and they are often different.
	contacts := []struct{ kind, value string }{
		{"phone", p.Phone}, {"email", p.Email}, {"line_id", p.LineID},
	}
	for _, c := range contacts {
		if c.value == "" {
			continue
		}
		if err := upsertContact(d, parentID, c.kind, c.value); err != nil {
			return "", err
		}
	}

	if _, err := d.Exec(`INSERT INTO notification_preference (parent_id) VALUES (?)
		ON CONFLICT (parent_id) DO NOTHING`, parentID); err != nil {
		return "", fmt.Errorf("notification preference for %s: %w", p.Email, err)
	}
	return parentID, nil
}

func upsertContact(d *sql.DB, parentID, kind, value string) error {
	var id string
	err := d.QueryRow(`SELECT parent_contact_id FROM parent_contact
		WHERE parent_id = ? AND contact_type = ?`, parentID, kind).Scan(&id)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		_, err := d.Exec(`INSERT INTO parent_contact (parent_contact_id, parent_id, contact_type, value)
			VALUES (?,?,?,?)`, parentID+"_"+kind, parentID, kind, value)
		return err
	case err != nil:
		return err
	}
	_, err = d.Exec(`UPDATE parent_contact SET value = ? WHERE parent_contact_id = ?`, value, id)
	return err
}

func ensureStudent(d *sql.DB, st RosterStudent, parentID, classID, hash string, today time.Time) error {
	accountID, err := ensureAccount(d, st.Email, "Student", st.Name, hash, "usr")
	if err != nil {
		return err
	}
	day := func(offset int) string { return today.AddDate(0, 0, offset).Format("2006-01-02") }
	lastAttended := day(-st.LastAttendedDaysAgo)

	var studentID string
	err = d.QueryRow(`SELECT student_id FROM student WHERE user_account_id = ?`, accountID).Scan(&studentID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		studentID = slugID("stu", st.Email)
		if _, err := d.Exec(`INSERT INTO student (student_id, user_account_id, name, date_of_birth,
			current_level, last_attended_date, streak_count) VALUES (?,?,?,?,?,?,?)`,
			studentID, accountID, st.Name, st.DateOfBirth, st.Level, lastAttended, st.Streak); err != nil {
			return fmt.Errorf("create student %s: %w", st.Name, err)
		}
	case err != nil:
		return fmt.Errorf("look up student %s: %w", st.Name, err)
	default:
		if _, err := d.Exec(`UPDATE student SET name = ?, date_of_birth = ?, current_level = ?,
			last_attended_date = ?, streak_count = ? WHERE student_id = ?`,
			st.Name, st.DateOfBirth, st.Level, lastAttended, st.Streak, studentID); err != nil {
			return fmt.Errorf("update student %s: %w", st.Name, err)
		}
	}

	if _, err := d.Exec(`INSERT INTO student_parent (student_id, parent_id, relationship_type)
		VALUES (?,?,?) ON CONFLICT (student_id, parent_id) DO UPDATE SET relationship_type = excluded.relationship_type`,
		studentID, parentID, st.Relation); err != nil {
		return fmt.Errorf("link %s to their parent: %w", st.Name, err)
	}
	if _, err := d.Exec(`INSERT INTO practice_settings (student_id) VALUES (?)
		ON CONFLICT (student_id) DO NOTHING`, studentID); err != nil {
		return fmt.Errorf("practice settings for %s: %w", st.Name, err)
	}

	var enrollmentID string
	err = d.QueryRow(`SELECT enrollment_id FROM student_enrollment WHERE student_id = ? AND class_id = ?`,
		studentID, classID).Scan(&enrollmentID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		enrollmentID = slugID("enr", st.Email)
		if _, err := d.Exec(`INSERT INTO student_enrollment (enrollment_id, student_id, class_id, enrolled_date, status)
			VALUES (?,?,?,?,'Active')`, enrollmentID, studentID, classID, st.EnrolledDate); err != nil {
			return fmt.Errorf("enrol %s: %w", st.Name, err)
		}
	case err != nil:
		return fmt.Errorf("look up enrollment for %s: %w", st.Name, err)
	}

	// One purchase carries the whole balance. The console sums the enrollment's
	// transactions for the credit figure and takes the latest expiry_date, so a
	// single row reproduces both numbers.
	txID := slugID("ctx", st.Email) + "_purchase"
	expiry := day(st.ExpiresInDays)
	res, err := d.Exec(`UPDATE credit_transaction SET amount = ?, expiry_date = ?
		WHERE credit_transaction_id = ?`, st.Credits, expiry, txID)
	if err != nil {
		return fmt.Errorf("update credits for %s: %w", st.Name, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if _, err := d.Exec(`INSERT INTO credit_transaction (credit_transaction_id, enrollment_id,
			transaction_type, amount, expiry_date, transaction_date, notes)
			VALUES (?,?,'purchase',?,?,?,'Opening balance imported with the roster')`,
			txID, enrollmentID, st.Credits, expiry, st.EnrolledDate); err != nil {
			return fmt.Errorf("credit %s: %w", st.Name, err)
		}
	}
	return nil
}
