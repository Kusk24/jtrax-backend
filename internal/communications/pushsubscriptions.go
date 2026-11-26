// Device and browser push subscriptions for the signed-in account.
package communications

import (
	"database/sql"
	"encoding/base64"
	"net/http"
	"strings"

	"github.com/Kusk24/jtrax-backend/internal/accounts"
	"github.com/Kusk24/jtrax-backend/internal/db"
	"github.com/Kusk24/jtrax-backend/internal/httpx"
	"github.com/Kusk24/jtrax-backend/internal/notify"
	"github.com/Kusk24/jtrax-backend/internal/push"
	"github.com/Kusk24/jtrax-backend/internal/webpush"
)

type pushInput struct {
	Channel   string `json:"channel"` // "webpush" or "mobile"
	Endpoint  string `json:"endpoint"`
	P256dh    string `json:"p256dh"`
	Auth      string `json:"auth"`
	UserAgent string `json:"user_agent"`
}

func handleRegisterPush(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := accounts.RequireIdentity(d, w, r)
		if id == nil {
			return
		}
		var in pushInput
		if err := httpx.Decode(r, &in); err != nil {
			httpx.Error(w, http.StatusBadRequest, "invalid body", err)
			return
		}
		if (in.Channel != notify.ChannelWebPush && in.Channel != notify.ChannelMobile) || in.Endpoint == "" {
			httpx.Error(w, http.StatusBadRequest, "channel must be webpush or mobile, with an endpoint", nil)
			return
		}
		// A phone registers an Expo push token and nothing else: that is the
		// only kind the sender can deliver to, and the column is not a place
		// to park whatever a client sends.
		if in.Channel == notify.ChannelMobile && (len(in.Endpoint) > 512 || !push.IsExpoToken(in.Endpoint)) {
			httpx.Error(w, http.StatusBadRequest, "that is not a push token this server can use", nil)
			return
		}
		// A browser's endpoint is a URL this server will POST to, so it has to
		// be a real push service's — never an address of the caller's choosing
		// — and come with the two keys a message is encrypted with.
		if in.Channel == notify.ChannelWebPush &&
			(len(in.Endpoint) > 2048 || !webpush.AllowedEndpoint(in.Endpoint) || !keyOfLength(in.P256dh, 65) || !keyOfLength(in.Auth, 16)) {
			httpx.Error(w, http.StatusBadRequest, "that is not a browser subscription this server can use", nil)
			return
		}
		// endpoint is UNIQUE: the same browser re-registering updates its owner
		// and last_seen_at rather than piling up rows. Re-binding to the current
		// caller also stops a stale row pointing at a previous account.
		if _, err := d.Exec(
			`INSERT INTO push_subscription
			   (push_subscription_id, user_account_id, channel, endpoint, p256dh, auth, user_agent)
			 VALUES (?,?,?,?,?,?,?)
			 ON CONFLICT(endpoint) DO UPDATE SET
			   user_account_id = excluded.user_account_id,
			   channel = excluded.channel, p256dh = excluded.p256dh, auth = excluded.auth,
			   user_agent = excluded.user_agent, last_seen_at = datetime('now'), failed_at = NULL`,
			db.NewID("psb"), id.UserAccountID, in.Channel, in.Endpoint, db.Nullable(in.P256dh), db.Nullable(in.Auth), db.Nullable(in.UserAgent)); err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not register", err)
			return
		}
		httpx.JSON(w, http.StatusCreated, map[string]any{"registered": true})
	}
}

// keyOfLength reports whether s is base64url (padded or not) for exactly n
// bytes — a subscription's p256dh is a 65-byte P-256 point, its auth 16 bytes.
func keyOfLength(s string, n int) bool {
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(s, "="))
	return err == nil && len(raw) == n
}

// handleWebPushKey hands a signed-in browser the public key it subscribes
// with. 404 while browser push is not set up, so the portal can leave the
// switch out rather than offer one that cannot work.
func handleWebPushKey(d *sql.DB, key string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if accounts.RequireIdentity(d, w, r) == nil {
			return
		}
		if key == "" {
			httpx.Error(w, http.StatusNotFound, "browser notifications are not set up", nil)
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"publicKey": key})
	}
}

func handleUnregisterPush(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := accounts.RequireIdentity(d, w, r)
		if id == nil {
			return
		}
		var in pushInput
		if err := httpx.Decode(r, &in); err != nil {
			httpx.Error(w, http.StatusBadRequest, "invalid body", err)
			return
		}
		// Scoped to the caller: you can only remove your own endpoint.
		if _, err := d.Exec(
			`DELETE FROM push_subscription WHERE endpoint = ? AND user_account_id = ?`,
			in.Endpoint, id.UserAccountID); err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not unregister", err)
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"unregistered": true})
	}
}
