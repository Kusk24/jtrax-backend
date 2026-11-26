// Staff settings for the LINE messaging channel.
package api

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/Kusk24/jtrax-backend/internal/httpx"
	"github.com/Kusk24/jtrax-backend/internal/line"
)

func handleLineChannelGet(l *lineDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := requireIdentity(l.db, w, r)
		if id == nil {
			return
		}
		if id.Role != "Admin" {
			httpx.Error(w, http.StatusForbidden, "not allowed", nil)
			return
		}
		out := map[string]any{
			"configured":    false,
			"sealingKeySet": l.box != nil,
			"webhookUrl":    lineWebhookURL(r),
		}
		var hint, updatedAt string
		err := l.db.QueryRow(`SELECT token_hint, updated_at FROM line_channel
		                      WHERE channel_row_id = 'default'`).Scan(&hint, &updatedAt)
		if err == nil {
			out["configured"] = true
			out["tokenHint"] = hint
			out["updatedAt"] = sqliteISO(updatedAt)
			// The remaining allowance is the real constraint on this feature,
			// so it is shown where the credentials are. A failure to read it is
			// not a failure of the page.
			if creds, err := l.creds(); err == nil {
				if limit, usedN, limited, err := l.newClient(creds.Token).Quota(); err == nil {
					out["quota"] = map[string]any{"limited": limited, "limit": limit, "used": usedN}
				} else {
					log.Printf("line: quota unavailable: %v", err)
				}
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			httpx.Error(w, http.StatusInternalServerError, "could not read settings", err)
			return
		}
		httpx.JSON(w, http.StatusOK, out)
	}
}

// lineWebhookURL is the address an operator pastes into the LINE console.
//
// PUBLIC_API_URL first: the console does not call this API from the browser
// but through its own server, on the internal address, so the request's host
// is 127.0.0.1:8790 — an address LINE can never reach, shown to an operator
// as the one to paste. Without it (development), the address this API was
// just reached on.
func lineWebhookURL(r *http.Request) string {
	if base := strings.TrimSuffix(strings.TrimSpace(os.Getenv("PUBLIC_API_URL")), "/"); base != "" {
		return base + "/api/v1/line/webhook"
	}
	scheme := "http"
	if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
		scheme = proto
	} else if r.TLS != nil {
		scheme = "https"
	}
	return fmt.Sprintf("%s://%s/api/v1/line/webhook", scheme, r.Host)
}

func handleLineChannelPut(l *lineDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := requireIdentity(l.db, w, r)
		if id == nil {
			return
		}
		if id.Role != "Admin" {
			httpx.Error(w, http.StatusForbidden, "not allowed", nil)
			return
		}
		if l.box == nil {
			httpx.Error(w, http.StatusServiceUnavailable,
				"the server has no sealing key, so credentials cannot be stored securely", errLineNoKey)
			return
		}
		var in struct {
			AccessToken   string `json:"accessToken"`
			ChannelSecret string `json:"channelSecret"`
		}
		if err := httpx.Decode(r, &in); err != nil {
			httpx.Error(w, http.StatusBadRequest, "accessToken and channelSecret are required", err)
			return
		}
		token := strings.TrimSpace(in.AccessToken)
		secret := strings.TrimSpace(in.ChannelSecret)
		if token == "" || secret == "" {
			httpx.Error(w, http.StatusBadRequest, "accessToken and channelSecret are required", nil)
			return
		}
		// Bounds at the boundary. A LINE access token is a few hundred
		// characters and a channel secret is 32 hex; these are generous caps
		// against a paste of something else entirely.
		if len(token) > 1000 || len(secret) > 200 {
			httpx.Error(w, http.StatusBadRequest, "that does not look like a LINE credential", nil)
			return
		}

		// Prove the token works before storing it. Only an outright rejection
		// counts: if LINE is unreachable the credential may well be fine, and
		// refusing to save during an outage helps nobody.
		if _, _, _, err := l.newClient(token).Quota(); err != nil {
			if line.ReasonOf(err) == line.ReasonInvalid {
				httpx.Error(w, http.StatusBadRequest, "LINE rejected that access token", err)
				return
			}
			log.Printf("line: could not verify token on save: %v", err)
		}

		tokenEnc, err := l.box.Seal(token)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not store credentials", err)
			return
		}
		secretEnc, err := l.box.Seal(secret)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not store credentials", err)
			return
		}
		hint := token
		if len(hint) > 4 {
			hint = hint[len(hint)-4:]
		}
		if _, err := l.db.Exec(`
			INSERT INTO line_channel (channel_row_id, access_token_enc, channel_secret_enc,
			                          token_hint, updated_at, updated_by)
			VALUES ('default', ?, ?, ?, ?, ?)
			ON CONFLICT (channel_row_id) DO UPDATE SET
			  access_token_enc = excluded.access_token_enc,
			  channel_secret_enc = excluded.channel_secret_enc,
			  token_hint = excluded.token_hint,
			  updated_at = excluded.updated_at,
			  updated_by = excluded.updated_by`,
			tokenEnc, secretEnc, hint, sqliteNow(), id.UserAccountID); err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not store credentials", err)
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"configured": true, "tokenHint": hint})
	}
}

func handleLineChannelDelete(l *lineDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := requireIdentity(l.db, w, r)
		if id == nil {
			return
		}
		if id.Role != "Admin" {
			httpx.Error(w, http.StatusForbidden, "not allowed", nil)
			return
		}
		if _, err := l.db.Exec(`DELETE FROM line_channel WHERE channel_row_id = 'default'`); err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not remove credentials", err)
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"configured": false})
	}
}

/* ---- mount ---- */
