// Notification endpoints: the in-app inbox, per-user channel preferences, push
// subscription registration, and the one manual trigger (credit-expiry) that a
// person sets off rather than a schedule.
//
// Every read is scoped by the caller's own user_account_id in the WHERE clause,
// never filtered afterwards — a parent must not be able to fetch another
// family's inbox. Push subscriptions are treated as per-user secrets: a
// caller's own are the only ones they can see or delete.
package communications

import (
	"database/sql"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/Kusk24/jtrax-backend/internal/accounts"
	"github.com/Kusk24/jtrax-backend/internal/db"
	"github.com/Kusk24/jtrax-backend/internal/httpx"
	"github.com/Kusk24/jtrax-backend/internal/notify"
	"github.com/Kusk24/jtrax-backend/internal/payments"
)

// MountNotifications wires the inbox, settings, subscriptions and the manual
// credit-expiry trigger. The service is shared with the registry hooks that
// fire on check-in and on a new announcement. webpushKey is the VAPID public
// key browsers subscribe with; empty while browser push is not set up.
func MountNotifications(mux gin.IRoutes, d *sql.DB, svc *notify.Service, webpushKey string) {
	mux.GET("/api/v1/notifications", handleListNotifications(d))
	mux.POST("/api/v1/notifications/:id/read", handleMarkRead(d))
	mux.POST("/api/v1/notifications/read-all", handleMarkAllRead(d))

	mux.GET("/api/v1/notification-settings", handleGetSettings(d))
	mux.PUT("/api/v1/notification-settings", handlePutSettings(d))

	mux.GET("/api/v1/push-subscriptions/webpush-key", handleWebPushKey(d, webpushKey))
	mux.POST("/api/v1/push-subscriptions", handleRegisterPush(d))
	mux.DELETE("/api/v1/push-subscriptions", handleUnregisterPush(d))

	// Manual, permission-gated: only Admin / Receptionist may set these off.
	mux.POST("/api/v1/notifications/credit-expiry", payments.HandleCreditExpiry(d, svc))
	mux.POST("/api/v1/notifications/low-credit", payments.HandleLowCredit(d, svc))
}

func handleListNotifications(d *sql.DB) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		id := accounts.RequireIdentity(d, ctx)
		if id == nil {
			return
		}
		// Scoped to the caller in the query itself.
		rows, err := d.Query(
			`SELECT notification_id, type, title, body, COALESCE(data,''), created_at, COALESCE(read_at,'')
			 FROM notification WHERE user_account_id = ?
			 ORDER BY created_at DESC, notification_id DESC LIMIT 100`, id.UserAccountID)
		if err != nil {
			httpx.Error(ctx, http.StatusInternalServerError, "could not load notifications", err)
			return
		}
		defer rows.Close()

		list := []map[string]any{}
		unread := 0
		for rows.Next() {
			var nid, typ, title, body, data, createdAt, readAt string
			if err := rows.Scan(&nid, &typ, &title, &body, &data, &createdAt, &readAt); err != nil {
				httpx.Error(ctx, http.StatusInternalServerError, "could not read notifications", err)
				return
			}
			if readAt == "" {
				unread++
			}
			list = append(list, map[string]any{
				"notification_id": nid, "type": typ, "title": title, "body": body,
				"data": data, "created_at": createdAt, "read_at": db.Nullable(readAt),
			})
		}
		httpx.JSON(ctx, http.StatusOK, map[string]any{"notifications": list, "unread": unread})
	}
}

func handleMarkRead(d *sql.DB) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		id := accounts.RequireIdentity(d, ctx)
		if id == nil {
			return
		}
		// The user_account_id predicate is the authorization: a caller can only
		// mark their own rows, and touching someone else's is a silent no-op.
		res, err := d.Exec(
			`UPDATE notification SET read_at = datetime('now')
			 WHERE notification_id = ? AND user_account_id = ? AND read_at IS NULL`,
			ctx.Param("id"), id.UserAccountID)
		if err != nil {
			httpx.Error(ctx, http.StatusInternalServerError, "could not mark read", err)
			return
		}
		n, _ := res.RowsAffected()
		httpx.JSON(ctx, http.StatusOK, map[string]any{"updated": n})
	}
}

func handleMarkAllRead(d *sql.DB) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		id := accounts.RequireIdentity(d, ctx)
		if id == nil {
			return
		}
		res, err := d.Exec(
			`UPDATE notification SET read_at = datetime('now')
			 WHERE user_account_id = ? AND read_at IS NULL`, id.UserAccountID)
		if err != nil {
			httpx.Error(ctx, http.StatusInternalServerError, "could not mark read", err)
			return
		}
		n, _ := res.RowsAffected()
		httpx.JSON(ctx, http.StatusOK, map[string]any{"updated": n})
	}
}

// handleGetSettings returns the caller's stored overrides. An absent (type,
// channel) means the default, which is on — the client fills the grid from
// that rule, so the response is only the exceptions.
func handleGetSettings(d *sql.DB) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		id := accounts.RequireIdentity(d, ctx)
		if id == nil {
			return
		}
		rows, err := d.Query(
			`SELECT type, channel, enabled FROM notification_setting WHERE user_account_id = ?`,
			id.UserAccountID)
		if err != nil {
			httpx.Error(ctx, http.StatusInternalServerError, "could not load settings", err)
			return
		}
		defer rows.Close()
		settings := []map[string]any{}
		for rows.Next() {
			var typ, channel string
			var enabled int
			if err := rows.Scan(&typ, &channel, &enabled); err != nil {
				httpx.Error(ctx, http.StatusInternalServerError, "could not read settings", err)
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
		httpx.JSON(ctx, http.StatusOK, map[string]any{
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
func handlePutSettings(d *sql.DB) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		id := accounts.RequireIdentity(d, ctx)
		if id == nil {
			return
		}
		var in settingInput
		if err := httpx.Decode(ctx, &in); err != nil {
			httpx.Error(ctx, http.StatusBadRequest, "invalid body", err)
			return
		}
		if !validType(in.Type) || !validChannel(in.Channel) {
			httpx.Error(ctx, http.StatusBadRequest, "unknown type or channel", nil)
			return
		}
		// The school's switch is the master: nobody opts in to a type the
		// school does not send. Opting out is always allowed.
		if in.Enabled && !notify.SchoolEnabled(d, in.Type) {
			httpx.Error(ctx, http.StatusConflict, "the school has turned this notification off", nil)
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
			httpx.Error(ctx, http.StatusInternalServerError, "could not save setting", err)
			return
		}
		httpx.JSON(ctx, http.StatusOK, map[string]any{"saved": true})
	}
}
