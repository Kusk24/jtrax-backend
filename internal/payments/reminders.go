// Credit reminders and notifications.
package payments

import (
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/Kusk24/jtrax-backend/internal/academytime"
	"github.com/Kusk24/jtrax-backend/internal/accounts"
	"github.com/Kusk24/jtrax-backend/internal/families"
	"github.com/Kusk24/jtrax-backend/internal/httpx"
	"github.com/Kusk24/jtrax-backend/internal/notify"
)

// HandleCreditExpiry notifies the parents of students whose credits expire
// within `days` (default 14). It is manual and permission-gated to staff — the
// academy wants a person to decide when this goes out, not a schedule.
//
// The optional JSON body refines the decision without moving the authority:
// `{"dry_run": true}` answers "who would this reach" — each affected student
// with their parents' names and the soonest expiry — and sends nothing;
// `{"student_ids": [...]}` narrows a real send to those students. The list can
// only shrink the eligible set: an id outside the expiry window is reported
// back under `skipped`, never sent to, so the caller cannot make this warn a
// family whose credits are fine.
func HandleCreditExpiry(d *sql.DB, svc *notify.Service) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		id := accounts.RequireIdentity(d, ctx)
		if id == nil {
			return
		}
		if !accounts.IsStaff(id.Role) {
			httpx.Error(ctx, http.StatusForbidden, "only admin or reception may send this", nil)
			return
		}
		days := 14
		if q := ctx.Query("days"); q != "" {
			if v, err := strconv.Atoi(q); err == nil && v > 0 && v <= 365 {
				days = v
			}
		}

		var req struct {
			DryRun     bool     `json:"dry_run"`
			StudentIDs []string `json:"student_ids"`
		}
		if ctx.Request.Body != nil {
			// An empty or absent body keeps the old behaviour: send to everyone.
			_ = json.NewDecoder(io.LimitReader(ctx.Request.Body, 1<<16)).Decode(&req)
		}
		if len(req.StudentIDs) > 500 {
			httpx.Error(ctx, http.StatusUnprocessableEntity, "too many student ids", nil)
			return
		}
		chosen := map[string]bool{}
		for _, sid := range req.StudentIDs {
			chosen[sid] = true
		}

		// Grouped so a student with several expiring lots is notified once,
		// about the soonest of them (creditreminders.go).
		targets, err := expiringTargets(d, days)
		if err != nil {
			httpx.Error(ctx, http.StatusInternalServerError, "could not find expiring credits", err)
			return
		}

		if req.DryRun {
			list := []map[string]any{}
			for _, t := range targets {
				list = append(list, map[string]any{
					"student_id":   t.studentID,
					"student_name": t.studentName,
					"parents":      families.ParentNamesOf(d, t.studentID),
					"expires":      t.expires,
				})
			}
			httpx.JSON(ctx, http.StatusOK, map[string]any{"targets": list, "within_days": days})
			return
		}

		// The selection can only narrow. Anything asked for that is not in the
		// eligible set goes back as skipped, so the desk sees the refusal
		// rather than believing a family was warned.
		eligible := map[string]bool{}
		for _, t := range targets {
			eligible[t.studentID] = true
		}
		skipped := []string{}
		for _, sid := range req.StudentIDs {
			if !eligible[sid] {
				skipped = append(skipped, sid)
			}
		}

		sent := 0
		for _, t := range targets {
			if len(chosen) > 0 && !chosen[t.studentID] {
				continue
			}
			recipients := families.ParentAccountsOf(d, t.studentID)
			if len(recipients) == 0 {
				continue
			}
			err := svc.Send(recipients, expiryMessage(t, days, "credit_expiry:"+t.studentID+":"+academytime.Today()))
			if err != nil {
				httpx.Error(ctx, http.StatusInternalServerError, "could not send", err)
				return
			}
			sent++
		}
		httpx.JSON(ctx, http.StatusOK, map[string]any{"students_notified": sent, "within_days": days, "skipped": skipped})
	}
}

// HandleLowCredit asks the parents of students whose credit is at or under the
// academy's low-credit line to top up. Manual and staff-only, the same shape as
// handleCreditExpiry: `{"dry_run": true}` lists who it would reach, and
// `{"student_ids": [...]}` narrows a send to those students. The list can only
// shrink the eligible set; an id outside it comes back under `skipped`.
//
// A balance is counted the way the console counts it: the credits recorded
// against an active course, plus the child's credits that sit in no course. A
// child in two courses is low if either course is.
func HandleLowCredit(d *sql.DB, svc *notify.Service) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		id := accounts.RequireIdentity(d, ctx)
		if id == nil {
			return
		}
		if !accounts.IsStaff(id.Role) {
			httpx.Error(ctx, http.StatusForbidden, "only admin or reception may send this", nil)
			return
		}
		var req struct {
			DryRun     bool     `json:"dry_run"`
			StudentIDs []string `json:"student_ids"`
		}
		if ctx.Request.Body != nil {
			_ = json.NewDecoder(io.LimitReader(ctx.Request.Body, 1<<16)).Decode(&req)
		}
		if len(req.StudentIDs) > 500 {
			httpx.Error(ctx, http.StatusUnprocessableEntity, "too many student ids", nil)
			return
		}

		targets, line, err := lowCreditTargets(d, "")
		if err != nil {
			httpx.Error(ctx, http.StatusInternalServerError, "could not read balances", err)
			return
		}
		lowest := map[string]bool{}
		for _, t := range targets {
			lowest[t.studentID] = true
		}

		if req.DryRun {
			list := []map[string]any{}
			for _, t := range targets {
				list = append(list, map[string]any{
					"student_id":   t.studentID,
					"student_name": t.studentName,
					"parents":      families.ParentNamesOf(d, t.studentID),
					"balance":      t.balance,
				})
			}
			httpx.JSON(ctx, http.StatusOK, map[string]any{"targets": list, "line": line})
			return
		}

		chosen := map[string]bool{}
		for _, sid := range req.StudentIDs {
			chosen[sid] = true
		}
		skipped := []string{}
		for _, sid := range req.StudentIDs {
			if !lowest[sid] {
				skipped = append(skipped, sid)
			}
		}

		sent := 0
		for _, t := range targets {
			if len(chosen) > 0 && !chosen[t.studentID] {
				continue
			}
			recipients := families.ParentAccountsOf(d, t.studentID)
			if len(recipients) == 0 {
				continue
			}
			// Pressing Send twice in a day reaches a family once.
			err := svc.Send(recipients, lowCreditMessage(t, "low_credit:"+t.studentID+":"+academytime.Today()))
			if err != nil {
				httpx.Error(ctx, http.StatusInternalServerError, "could not send", err)
				return
			}
			sent++
		}
		httpx.JSON(ctx, http.StatusOK, map[string]any{"students_notified": sent, "line": line, "skipped": skipped})
	}
}
