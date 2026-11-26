// Notification endpoints: the in-app inbox, per-user channel preferences, push
// subscription registration, and the one manual trigger (credit-expiry) that a
// person sets off rather than a schedule.
//
// Every read is scoped by the caller's own user_account_id in the WHERE clause,
// never filtered afterwards — a parent must not be able to fetch another
// family's inbox. Push subscriptions are treated as per-user secrets: a
// caller's own are the only ones they can see or delete.
package api

import (
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"strconv"

	"github.com/Kusk24/jtrax-backend/internal/httpx"
	"github.com/Kusk24/jtrax-backend/internal/notify"
)

// mountNotifications wires the inbox, settings, subscriptions and the manual
// credit-expiry trigger. The service is shared with the registry hooks that
// fire on check-in and on a new announcement. webpushKey is the VAPID public
// key browsers subscribe with; empty while browser push is not set up.
func mountNotifications(mux routeRegistrar, d *sql.DB, svc *notify.Service, webpushKey string) {
	mux.HandleFunc("GET /api/v1/notifications", handleListNotifications(d))
	mux.HandleFunc("POST /api/v1/notifications/{id}/read", handleMarkRead(d))
	mux.HandleFunc("POST /api/v1/notifications/read-all", handleMarkAllRead(d))

	mux.HandleFunc("GET /api/v1/notification-settings", handleGetSettings(d))
	mux.HandleFunc("PUT /api/v1/notification-settings", handlePutSettings(d))

	mux.HandleFunc("GET /api/v1/push-subscriptions/webpush-key", handleWebPushKey(d, webpushKey))
	mux.HandleFunc("POST /api/v1/push-subscriptions", handleRegisterPush(d))
	mux.HandleFunc("DELETE /api/v1/push-subscriptions", handleUnregisterPush(d))

	// Manual, permission-gated: only Admin / Receptionist may set these off.
	mux.HandleFunc("POST /api/v1/notifications/credit-expiry", handleCreditExpiry(d, svc))
	mux.HandleFunc("POST /api/v1/notifications/low-credit", handleLowCredit(d, svc))
}

// ---- inbox ---------------------------------------------------------------

func handleListNotifications(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := requireIdentity(d, w, r)
		if id == nil {
			return
		}
		// Scoped to the caller in the query itself.
		rows, err := d.Query(
			`SELECT notification_id, type, title, body, COALESCE(data,''), created_at, COALESCE(read_at,'')
			 FROM notification WHERE user_account_id = ?
			 ORDER BY created_at DESC, notification_id DESC LIMIT 100`, id.UserAccountID)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not load notifications", err)
			return
		}
		defer rows.Close()

		list := []map[string]any{}
		unread := 0
		for rows.Next() {
			var nid, typ, title, body, data, createdAt, readAt string
			if err := rows.Scan(&nid, &typ, &title, &body, &data, &createdAt, &readAt); err != nil {
				httpx.Error(w, http.StatusInternalServerError, "could not read notifications", err)
				return
			}
			if readAt == "" {
				unread++
			}
			list = append(list, map[string]any{
				"notification_id": nid, "type": typ, "title": title, "body": body,
				"data": data, "created_at": createdAt, "read_at": nullable(readAt),
			})
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"notifications": list, "unread": unread})
	}
}

func handleMarkRead(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := requireIdentity(d, w, r)
		if id == nil {
			return
		}
		// The user_account_id predicate is the authorization: a caller can only
		// mark their own rows, and touching someone else's is a silent no-op.
		res, err := d.Exec(
			`UPDATE notification SET read_at = datetime('now')
			 WHERE notification_id = ? AND user_account_id = ? AND read_at IS NULL`,
			r.PathValue("id"), id.UserAccountID)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not mark read", err)
			return
		}
		n, _ := res.RowsAffected()
		httpx.JSON(w, http.StatusOK, map[string]any{"updated": n})
	}
}

func handleMarkAllRead(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := requireIdentity(d, w, r)
		if id == nil {
			return
		}
		res, err := d.Exec(
			`UPDATE notification SET read_at = datetime('now')
			 WHERE user_account_id = ? AND read_at IS NULL`, id.UserAccountID)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not mark read", err)
			return
		}
		n, _ := res.RowsAffected()
		httpx.JSON(w, http.StatusOK, map[string]any{"updated": n})
	}
}

// ---- preferences ---------------------------------------------------------

// handleGetSettings returns the caller's stored overrides. An absent (type,
// channel) means the default, which is on — the client fills the grid from
// that rule, so the response is only the exceptions.
func handleGetSettings(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := requireIdentity(d, w, r)
		if id == nil {
			return
		}
		rows, err := d.Query(
			`SELECT type, channel, enabled FROM notification_setting WHERE user_account_id = ?`,
			id.UserAccountID)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not load settings", err)
			return
		}
		defer rows.Close()
		settings := []map[string]any{}
		for rows.Next() {
			var typ, channel string
			var enabled int
			if err := rows.Scan(&typ, &channel, &enabled); err != nil {
				httpx.Error(w, http.StatusInternalServerError, "could not read settings", err)
				return
			}
			settings = append(settings, map[string]any{"type": typ, "channel": channel, "enabled": enabled != 0})
		}
		/* The school-level switch for every type, beside this person's own
		   choices. The two are different settings: the school's says whether
		   JTrax sends the type at all, the person's whether they want it. A
		   type the school has off is simply not offered — the person's saved
		   choice is kept, and applies again when the school turns it back on. */
		school := map[string]bool{}
		/* How each type can reach the person besides the bell: only payment
		   receipts and announcements are emailed and pushed, so only those
		   are offered email, phone and browser switches. */
		channelsByType := map[string][]string{}
		for _, typ := range allTypes {
			school[typ] = notify.SchoolEnabled(d, typ)
			channelsByType[typ] = notify.ChannelsFor(typ)
		}
		httpx.JSON(w, http.StatusOK, map[string]any{
			"settings":       settings,
			"types":          []string{notify.TypeCheckIn, notify.TypeCheckOut, notify.TypeCreditExpiry, notify.TypeAnnouncement, notify.TypeClassCancelled},
			"channels":       notify.Channels,
			"channelsByType": channelsByType,
			"schoolEnabled":  school,
		})
	}
}

type settingInput struct {
	Type    string `json:"type"`
	Channel string `json:"channel"`
	Enabled bool   `json:"enabled"`
}

// handlePutSettings upserts one override for the caller. The in-app row for a
// type is that person's master toggle — most alert types are in-app only, so
// "Check-in Alerts: off" has to mean off, and the opt-in types (low credit)
// are turned on the same way.
func handlePutSettings(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := requireIdentity(d, w, r)
		if id == nil {
			return
		}
		var in settingInput
		if err := httpx.Decode(r, &in); err != nil {
			httpx.Error(w, http.StatusBadRequest, "invalid body", err)
			return
		}
		if !validType(in.Type) || !validChannel(in.Channel) {
			httpx.Error(w, http.StatusBadRequest, "unknown type or channel", nil)
			return
		}
		// The school's switch is the master: nobody opts in to a type the
		// school does not send. Opting out is always allowed.
		if in.Enabled && !notify.SchoolEnabled(d, in.Type) {
			httpx.Error(w, http.StatusConflict, "the school has turned this notification off", nil)
			return
		}
		enabled := 0
		if in.Enabled {
			enabled = 1
		}
		if _, err := d.Exec(
			`INSERT INTO notification_setting (user_account_id, type, channel, enabled)
			 VALUES (?,?,?,?)
			 ON CONFLICT(user_account_id, type, channel) DO UPDATE SET enabled = excluded.enabled`,
			id.UserAccountID, in.Type, in.Channel, enabled); err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not save setting", err)
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"saved": true})
	}
}

// ---- push subscriptions --------------------------------------------------

// handleCreditExpiry notifies the parents of students whose credits expire
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
func handleCreditExpiry(d *sql.DB, svc *notify.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := requireIdentity(d, w, r)
		if id == nil {
			return
		}
		if !isStaff(id.Role) {
			httpx.Error(w, http.StatusForbidden, "only admin or reception may send this", nil)
			return
		}
		days := 14
		if q := r.URL.Query().Get("days"); q != "" {
			if v, err := strconv.Atoi(q); err == nil && v > 0 && v <= 365 {
				days = v
			}
		}

		var req struct {
			DryRun     bool     `json:"dry_run"`
			StudentIDs []string `json:"student_ids"`
		}
		if r.Body != nil {
			// An empty or absent body keeps the old behaviour: send to everyone.
			_ = json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req)
		}
		if len(req.StudentIDs) > 500 {
			httpx.Error(w, http.StatusUnprocessableEntity, "too many student ids", nil)
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
			httpx.Error(w, http.StatusInternalServerError, "could not find expiring credits", err)
			return
		}

		if req.DryRun {
			list := []map[string]any{}
			for _, t := range targets {
				list = append(list, map[string]any{
					"student_id":   t.studentID,
					"student_name": t.studentName,
					"parents":      parentNamesOf(d, t.studentID),
					"expires":      t.expires,
				})
			}
			httpx.JSON(w, http.StatusOK, map[string]any{"targets": list, "within_days": days})
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
			recipients := parentAccountsOf(d, t.studentID)
			if len(recipients) == 0 {
				continue
			}
			err := svc.Send(recipients, expiryMessage(t, days, "credit_expiry:"+t.studentID+":"+today()))
			if err != nil {
				httpx.Error(w, http.StatusInternalServerError, "could not send", err)
				return
			}
			sent++
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"students_notified": sent, "within_days": days, "skipped": skipped})
	}
}

// handleLowCredit asks the parents of students whose credit is at or under the
// academy's low-credit line to top up. Manual and staff-only, the same shape as
// handleCreditExpiry: `{"dry_run": true}` lists who it would reach, and
// `{"student_ids": [...]}` narrows a send to those students. The list can only
// shrink the eligible set; an id outside it comes back under `skipped`.
//
// A balance is counted the way the console counts it: the credits recorded
// against an active course, plus the child's credits that sit in no course. A
// child in two courses is low if either course is.
func handleLowCredit(d *sql.DB, svc *notify.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := requireIdentity(d, w, r)
		if id == nil {
			return
		}
		if !isStaff(id.Role) {
			httpx.Error(w, http.StatusForbidden, "only admin or reception may send this", nil)
			return
		}
		var req struct {
			DryRun     bool     `json:"dry_run"`
			StudentIDs []string `json:"student_ids"`
		}
		if r.Body != nil {
			_ = json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req)
		}
		if len(req.StudentIDs) > 500 {
			httpx.Error(w, http.StatusUnprocessableEntity, "too many student ids", nil)
			return
		}

		targets, line, err := lowCreditTargets(d, "")
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not read balances", err)
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
					"parents":      parentNamesOf(d, t.studentID),
					"balance":      t.balance,
				})
			}
			httpx.JSON(w, http.StatusOK, map[string]any{"targets": list, "line": line})
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
			recipients := parentAccountsOf(d, t.studentID)
			if len(recipients) == 0 {
				continue
			}
			// Pressing Send twice in a day reaches a family once.
			err := svc.Send(recipients, lowCreditMessage(t, "low_credit:"+t.studentID+":"+today()))
			if err != nil {
				httpx.Error(w, http.StatusInternalServerError, "could not send", err)
				return
			}
			sent++
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"students_notified": sent, "line": line, "skipped": skipped})
	}
}

// ---- triggers on generic writes -----------------------------------------
