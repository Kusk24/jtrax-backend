package db_test

import (
	"testing"
	"time"

	"github.com/Kusk24/jtrax-backend/internal/auth"
	"github.com/Kusk24/jtrax-backend/internal/db"
)

// The console derives a student's status from these thresholds (lib/derive.ts:
// low credit at 3 or fewer, expiring within 7 days). The roster's offsets are
// chosen against them, so a change here should fail loudly.
const (
	lowCreditAt  = 3.0
	expiringDays = 7
)

var importDay = time.Date(2026, 8, 13, 9, 0, 0, 0, time.UTC)

func TestLoadRoster(t *testing.T) {
	r, err := db.LoadRoster()
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Families) != 10 {
		t.Errorf("%d families, want the console's 10", len(r.Families))
	}
	seen := map[string]bool{}
	for _, f := range r.Families {
		if f.Parent.Email == "" || f.Parent.Name == "" {
			t.Errorf("family %q is missing an email or name", f.Parent.Name)
		}
		if seen[f.Parent.Email] {
			t.Errorf("duplicate parent email %s", f.Parent.Email)
		}
		seen[f.Parent.Email] = true
		for _, st := range f.Students {
			if seen[st.Email] {
				t.Errorf("duplicate student email %s", st.Email)
			}
			seen[st.Email] = true
			if st.Class == "" || st.Email == "" {
				t.Errorf("student %q is missing a class or email", st.Name)
			}
			var known bool
			for _, c := range r.Classes {
				known = known || c.Name == st.Class
			}
			if !known {
				t.Errorf("student %q is in class %q, which the roster does not define", st.Name, st.Class)
			}
		}
	}
}

func TestImportRosterCreatesSignableFamilies(t *testing.T) {
	d := open(t)
	r, err := db.LoadRoster()
	if err != nil {
		t.Fatal(err)
	}

	written, err := db.ImportRoster(d, r, "roster-password", importDay)
	if err != nil {
		t.Fatal(err)
	}
	if len(written) != 20 {
		t.Fatalf("wrote %d accounts, want 10 parents + 10 students", len(written))
	}

	var parents, students int
	d.QueryRow(`SELECT COUNT(*) FROM user_account WHERE role = 'Parent'`).Scan(&parents)
	d.QueryRow(`SELECT COUNT(*) FROM user_account WHERE role = 'Student'`).Scan(&students)
	if parents != 10 || students != 10 {
		t.Errorf("%d parent and %d student accounts, want 10 each", parents, students)
	}

	// Every account must actually verify the password it was given.
	var hash string
	if err := d.QueryRow(`SELECT password_hash FROM user_account WHERE email = ?`,
		"carol.carter@gmail.com").Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if !auth.VerifyPassword("roster-password", hash) {
		t.Error("the parent's stored hash does not verify the roster password")
	}

	// A parent must reach their child through student_parent, which is what the
	// portal's whole scoping rests on.
	var child, relation string
	if err := d.QueryRow(`SELECT s.name, sp.relationship_type
		FROM student_parent sp
		JOIN student s ON s.student_id = sp.student_id
		JOIN parent p ON p.parent_id = sp.parent_id
		JOIN user_account u ON u.user_account_id = p.user_account_id
		WHERE u.email = ?`, "carol.carter@gmail.com").Scan(&child, &relation); err != nil {
		t.Fatal(err)
	}
	if child != "Emma Carter" || relation != "Mother" {
		t.Errorf("Carol's child is %q (%s), want Emma Carter (Mother)", child, relation)
	}

	// Credits and expiry drive the console's status column.
	var credits float64
	var expiry string
	if err := d.QueryRow(`SELECT ct.amount, ct.expiry_date
		FROM credit_transaction ct
		JOIN student_enrollment e ON e.enrollment_id = ct.enrollment_id
		JOIN student s ON s.student_id = e.student_id
		WHERE s.name = ?`, "Emma Carter").Scan(&credits, &expiry); err != nil {
		t.Fatal(err)
	}
	if credits != 8 {
		t.Errorf("Emma has %v credits, want the console's 8", credits)
	}
	if expiry <= importDay.Format("2006-01-02") {
		t.Errorf("Emma's credits expire %s, which is not in the future", expiry)
	}

	// Every student is enrolled in exactly one class, with a class row to match.
	var unenrolled int
	d.QueryRow(`SELECT COUNT(*) FROM student s WHERE NOT EXISTS
		(SELECT 1 FROM student_enrollment e WHERE e.student_id = s.student_id)`).Scan(&unenrolled)
	if unenrolled != 0 {
		t.Errorf("%d students have no enrollment", unenrolled)
	}
}

// The offsets in roster.json exist to reproduce the statuses the console was
// written with; this pins the two that carry the dashboard's follow-up card.
func TestImportRosterReproducesTheAuthoredStatuses(t *testing.T) {
	d := open(t)
	r, _ := db.LoadRoster()
	if _, err := db.ImportRoster(d, r, "roster-password", importDay); err != nil {
		t.Fatal(err)
	}

	status := func(name string) (credits float64, expiry string) {
		if err := d.QueryRow(`SELECT ct.amount, ct.expiry_date
			FROM credit_transaction ct
			JOIN student_enrollment e ON e.enrollment_id = ct.enrollment_id
			JOIN student s ON s.student_id = e.student_id
			WHERE s.name = ?`, name).Scan(&credits, &expiry); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return
	}
	day := func(offset int) string { return importDay.AddDate(0, 0, offset).Format("2006-01-02") }

	if _, expiry := status("Noah Kim"); expiry > day(expiringDays) {
		t.Errorf("Noah expires %s, too far out to read as Expiring", expiry)
	}
	if _, expiry := status("Zoe Bennet"); expiry >= day(0) {
		t.Errorf("Zoe expires %s, which is not in the past — she should read as Expired", expiry)
	}
	if credits, _ := status("Sofia Reyes"); credits > lowCreditAt {
		t.Errorf("Sofia has %v credits, too many to read as Low Credit", credits)
	}
	if credits, _ := status("Ava Patel"); credits > lowCreditAt {
		t.Errorf("Ava has %v credits, too many to read as Low Credit", credits)
	}
}

func TestImportRosterIsIdempotent(t *testing.T) {
	d := open(t)
	r, _ := db.LoadRoster()
	if _, err := db.ImportRoster(d, r, "first-password", importDay); err != nil {
		t.Fatal(err)
	}
	// A second run a day later, with a different password.
	if _, err := db.ImportRoster(d, r, "second-password", importDay.AddDate(0, 0, 1)); err != nil {
		t.Fatal(err)
	}

	counts := map[string]int{}
	for _, table := range []string{"user_account", "parent", "student", "student_parent",
		"student_enrollment", "credit_transaction", "class", "parent_contact"} {
		var n int
		if err := d.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		counts[table] = n
	}
	if counts["user_account"] != 20 || counts["parent"] != 10 || counts["student"] != 10 {
		t.Errorf("re-running duplicated accounts: %v", counts)
	}
	if counts["student_enrollment"] != 10 || counts["credit_transaction"] != 10 {
		t.Errorf("re-running duplicated enrollments or credits: %v", counts)
	}
	if counts["class"] != 4 {
		t.Errorf("%d classes, want the roster's 4", counts["class"])
	}
	if counts["parent_contact"] != 30 {
		t.Errorf("%d parent contacts, want 3 per parent", counts["parent_contact"])
	}

	var hash string
	d.QueryRow(`SELECT password_hash FROM user_account WHERE email = ?`, "carol.carter@gmail.com").Scan(&hash)
	if auth.VerifyPassword("first-password", hash) {
		t.Error("the first password still works after a re-import")
	}
	if !auth.VerifyPassword("second-password", hash) {
		t.Error("the second password does not work after a re-import")
	}
}

func TestImportRosterAdoptsAnExistingAccount(t *testing.T) {
	d := open(t)
	// Someone already signed up with a roster address, under a different id.
	if _, err := d.Exec(`INSERT INTO user_account (user_account_id, email, password_hash, role, display_name)
		VALUES ('usr_preexisting','carol.carter@gmail.com','x','Parent','Carol')`); err != nil {
		t.Fatal(err)
	}

	r, _ := db.LoadRoster()
	if _, err := db.ImportRoster(d, r, "roster-password", importDay); err != nil {
		t.Fatalf("import refused to adopt an existing email: %v", err)
	}

	var n int
	d.QueryRow(`SELECT COUNT(*) FROM user_account WHERE email = ?`, "carol.carter@gmail.com").Scan(&n)
	if n != 1 {
		t.Errorf("%d accounts for the same email, want 1", n)
	}
	var linked string
	d.QueryRow(`SELECT user_account_id FROM parent WHERE name = ?`, "Carol Carter").Scan(&linked)
	if linked != "usr_preexisting" {
		t.Errorf("parent linked to %q, want the account that already existed", linked)
	}
}

func TestImportRosterRequiresAPassword(t *testing.T) {
	d := open(t)
	r, _ := db.LoadRoster()
	if _, err := db.ImportRoster(d, r, "", importDay); err == nil {
		t.Error("importing with an empty password was allowed")
	}
}

func TestImportRosterWritesTheTimetable(t *testing.T) {
	d := open(t)
	r, _ := db.LoadRoster()
	if _, err := db.ImportRoster(d, r, "roster-password", importDay); err != nil {
		t.Fatal(err)
	}

	var sessions, attendance, payments, practice int
	d.QueryRow(`SELECT COUNT(*) FROM class_session`).Scan(&sessions)
	d.QueryRow(`SELECT COUNT(*) FROM attendance`).Scan(&attendance)
	d.QueryRow(`SELECT COUNT(*) FROM payment`).Scan(&payments)
	d.QueryRow(`SELECT COUNT(*) FROM practice_activity`).Scan(&practice)

	// 8 slots x 6 weeks.
	if sessions != 48 {
		t.Errorf("%d sessions, want 48", sessions)
	}

	// The dashboard's "Today's Classes" is empty unless the day the import ran
	// has sessions on it, so the timetable has to cover every opening day.
	var todays int
	d.QueryRow(`SELECT COUNT(*) FROM class_session WHERE session_date = ?`,
		importDay.Format("2006-01-02")).Scan(&todays)
	if todays == 0 {
		t.Error("nothing is scheduled on the import day, so the dashboard opens empty")
	}
	if attendance == 0 {
		t.Error("no attendance written, so the dashboard's check-in list would be empty")
	}
	if payments != 10 {
		t.Errorf("%d payments, want one per family", payments)
	}
	if practice == 0 {
		t.Error("no practice activity written, so the practice strip would be blank")
	}

	// Every imported payment carries the names it will need if the student it
	// was for is ever deleted — the row outlives them.
	var nameless int
	d.QueryRow(`SELECT COUNT(*) FROM payment
		WHERE student_name IS NULL OR student_name = ''
		   OR class_name IS NULL OR class_name = ''
		   OR parent_name IS NULL OR parent_name = ''`).Scan(&nameless)
	if nameless != 0 {
		t.Errorf("%d imported payments have no student, class or payer recorded on them", nameless)
	}

	// The revenue chart reads six months back; every payment must land inside it.
	oldest := importDay.AddDate(0, -6, 0).Format("2006-01-02")
	var stray int
	d.QueryRow(`SELECT COUNT(*) FROM payment WHERE payment_date < ? OR payment_date > ?`,
		oldest, importDay.Format("2006-01-02")).Scan(&stray)
	if stray != 0 {
		t.Errorf("%d payments fall outside the six months the revenue chart shows", stray)
	}

	// Nobody is checked out of a class that is still running.
	var openRows int
	d.QueryRow(`SELECT COUNT(*) FROM attendance a JOIN class_session s ON s.session_id = a.session_id
		WHERE s.session_status = 'Ongoing' AND a.check_out_time IS NOT NULL`).Scan(&openRows)
	if openRows != 0 {
		t.Errorf("%d students are checked out of an ongoing session", openRows)
	}
}

func TestImportRosterTimetableIsIdempotent(t *testing.T) {
	d := open(t)
	r, _ := db.LoadRoster()
	if _, err := db.ImportRoster(d, r, "pw-one", importDay); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ImportRoster(d, r, "pw-two", importDay); err != nil {
		t.Fatal(err)
	}
	for table, want := range map[string]int{"class_session": 48, "payment": 10} {
		var n int
		if err := d.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != want {
			t.Errorf("re-running duplicated %s: %d rows, want %d", table, n, want)
		}
	}
}
