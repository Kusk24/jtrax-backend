// Credit reminders and notifications.
package payments

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/Kusk24/jtrax-backend/internal/accounts"
	"github.com/Kusk24/jtrax-backend/internal/auth"
	"github.com/Kusk24/jtrax-backend/internal/db"
	"github.com/Kusk24/jtrax-backend/internal/httpx"
	"github.com/Kusk24/jtrax-backend/internal/records"
)

// voidReasons are the reasons a payment can be voided for.
var voidReasons = []string{"duplicate", "wrong_payer", "other"}

// minVoidNote is the shortest explanation accepted for "other".
const minVoidNote = 5

func auditValue(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}

// AuditPaymentEdit refuses an edit to a voided payment and records what an
// edit changes. Runs in the edit's transaction.
func AuditPaymentEdit(tx *sql.Tx, caller *auth.Identity, paymentID string, existing, patch map[string]any) error {
	if auditValue(existing["status"]) == "Voided" {
		return &records.ClientError{Status: http.StatusConflict, Message: "a voided payment can't be edited"}
	}
	changes := map[string]map[string]any{}
	for field, v := range patch {
		if auditValue(existing[field]) != auditValue(v) {
			changes[field] = map[string]any{"from": existing[field], "to": v}
		}
	}
	if len(changes) == 0 {
		return nil
	}
	raw, _ := json.Marshal(changes)
	_, err := tx.Exec(`INSERT INTO payment_audit (audit_id, payment_id, action, changed_by, changed_by_name, changed_at, changes)
	                   VALUES (?,?,?,?,?,?,?)`,
		db.NewID("pau"), paymentID, "edit", caller.UserAccountID, caller.DisplayName,
		time.Now().UTC().Format(time.RFC3339), string(raw))
	return err
}

// handleVoidPayment voids a payment: admins only, with a reason, once.
func handleVoidPayment(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		caller := accounts.RequireIdentity(d, w, r)
		if caller == nil {
			return
		}
		if caller.Role != "Admin" {
			httpx.Error(w, http.StatusForbidden, "only an admin can void a payment", nil)
			return
		}
		var in struct {
			Reason string `json:"reason"`
			Note   string `json:"note"`
		}
		if err := httpx.Decode(r, &in); err != nil || !slices.Contains(voidReasons, in.Reason) {
			httpx.Error(w, http.StatusBadRequest, "choose a reason for voiding", err)
			return
		}
		note := strings.TrimSpace(in.Note)
		if in.Reason == "other" && len([]rune(note)) < minVoidNote {
			httpx.Error(w, http.StatusBadRequest, "explain the reason for voiding", nil)
			return
		}
		if len([]rune(note)) > 500 {
			httpx.Error(w, http.StatusBadRequest, "the reason is too long", nil)
			return
		}
		paymentID := r.PathValue("id")
		reversed, err := VoidPayment(d, caller, paymentID, in.Reason, note)
		switch {
		case errors.Is(err, ErrNotFound):
			httpx.Error(w, http.StatusNotFound, err.Error(), nil)
			return
		case errors.Is(err, ErrAlreadyVoided):
			httpx.Error(w, http.StatusConflict, err.Error(), nil)
			return
		case err != nil:
			httpx.Error(w, http.StatusInternalServerError, "could not void the payment", err)
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"payment_id": paymentID, "status": "Voided", "credits_reversed": reversed})
	}
}

// handlePaymentHistory is a payment's audit trail, oldest first. Staff only.
func handlePaymentHistory(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if accounts.RequireStaff(d, w, r) == nil {
			return
		}
		rows, err := d.Query(`SELECT action, COALESCE(changed_by_name, ''), changed_at, COALESCE(changes, ''), COALESCE(reason, '')
		                        FROM payment_audit WHERE payment_id = ? ORDER BY changed_at, rowid`, r.PathValue("id"))
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not read the history", err)
			return
		}
		defer rows.Close()
		out := []map[string]any{}
		for rows.Next() {
			var action, by, at, changes, reason string
			if err := rows.Scan(&action, &by, &at, &changes, &reason); err != nil {
				httpx.Error(w, http.StatusInternalServerError, "could not read the history", err)
				return
			}
			entry := map[string]any{"action": action, "by": by, "at": at, "reason": reason}
			var parsed any
			if json.Unmarshal([]byte(changes), &parsed) == nil {
				entry["changes"] = parsed
			}
			out = append(out, entry)
		}
		httpx.JSON(w, http.StatusOK, out)
	}
}

func MountPaymentVoid(mux httpx.RouteRegistrar, d *sql.DB) {
	mux.HandleFunc("POST /api/v1/payments/{id}/void", handleVoidPayment(d))
	mux.HandleFunc("GET /api/v1/payments/{id}/history", handlePaymentHistory(d))
}
