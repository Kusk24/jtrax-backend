// A rejected audit row must roll back the entire correction, including its ledger adjustment.
package payments_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/Kusk24/jtrax-backend/internal/auth"
	"github.com/Kusk24/jtrax-backend/internal/db"
	"github.com/Kusk24/jtrax-backend/internal/payments"
)

func TestVoidRollsBackWhenAuditFails(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "school.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := db.Seed(d, db.DevPassword); err != nil {
		t.Fatal(err)
	}
	var admin auth.Identity
	if err := d.QueryRow("SELECT user_account_id, display_name, role FROM user_account WHERE role = 'Admin' LIMIT 1").Scan(&admin.UserAccountID, &admin.DisplayName, &admin.Role); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec("CREATE TRIGGER block_audit BEFORE INSERT ON payment_audit BEGIN SELECT RAISE(ABORT, 'audit unavailable'); END"); err != nil {
		t.Fatal(err)
	}

	_, err = payments.VoidPayment(d, &admin, "pay_1", "duplicate", "")
	if err == nil || !strings.Contains(err.Error(), "audit unavailable") {
		t.Fatalf("expected audit failure, got %v", err)
	}
	var status string
	var balance float64
	if err := d.QueryRow("SELECT status FROM payment WHERE payment_id = 'pay_1'").Scan(&status); err != nil {
		t.Fatal(err)
	}
	if err := d.QueryRow("SELECT SUM(amount) FROM credit_transaction WHERE payment_id = 'pay_1'").Scan(&balance); err != nil {
		t.Fatal(err)
	}
	if status != "Paid" || balance != 20 {
		t.Fatalf("after rollback: status %s, credits %v", status, balance)
	}
}
