// The LINE webhook verifies and applies incoming channel events.
package lineinbox

import (
	"database/sql"
	"errors"
	"io"
	"log"
	"net/http"

	"github.com/Kusk24/jtrax-backend/internal/db"
	"github.com/Kusk24/jtrax-backend/internal/httpx"
	"github.com/Kusk24/jtrax-backend/internal/line"
)

func handleLineWebhook(l *lineDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, lineMaxWebhookBytes))
		if err != nil {
			httpx.Error(w, http.StatusRequestEntityTooLarge, "payload too large", err)
			return
		}
		creds, err := l.creds()
		if err != nil {
			httpx.Error(w, http.StatusServiceUnavailable, "line messaging is not configured", err)
			return
		}
		// The signature is over the exact bytes received, which is why the body
		// is read raw and parsed only after this passes. Re-encoding parsed
		// JSON would change the digest and reject every genuine delivery.
		if !line.Verify(creds.Secret, body, r.Header.Get("X-Line-Signature")) {
			httpx.Error(w, http.StatusUnauthorized, "invalid signature", nil)
			return
		}
		events, err := line.ParseEvents(body)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, "malformed webhook", err)
			return
		}
		client := l.newClient(creds.Token)
		changed := ""
		for _, ev := range events {
			if l.applyEvent(ev, client) {
				changed = ev.Source.UserID
			}
		}
		if changed != "" {
			l.publishInbox(changed)
		}
		// LINE retries anything that is not a 2xx and disables a webhook that
		// keeps failing, so a batch that changed nothing still succeeds.
		httpx.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

// applyEvent records one webhook event, reporting whether anything changed.
func (l *lineDeps) applyEvent(ev line.Event, c lineSender) bool {
	// Only 1:1 chats. A group or room event has no personal userId to thread
	// against, and the academy's account is not in groups.
	if ev.Source.Type != "user" || ev.Source.UserID == "" {
		return false
	}
	uid := ev.Source.UserID

	switch ev.Type {
	case "unfollow":
		// Kept, not deleted: the history of what was said stays readable, and
		// the composer needs to know why sending will fail.
		if _, err := l.db.Exec(`UPDATE line_contact SET followed = 0 WHERE line_user_id = ?`, uid); err != nil {
			log.Printf("line: unfollow %s: %v", uid, err)
			return false
		}
		return true

	case "follow":
		l.upsertContact(uid, c)
		if _, err := l.db.Exec(`UPDATE line_contact
		                        SET followed = 1, reply_token = ?, reply_token_at = ?
		                        WHERE line_user_id = ?`, ev.ReplyToken, db.SQLiteNow(), uid); err != nil {
			log.Printf("line: follow %s: %v", uid, err)
			return false
		}
		return true

	case "message":
		l.upsertContact(uid, c)
		at := ev.At().Format(db.SQLiteTimeLayout)
		// OR IGNORE against the unique provider id: LINE guarantees at-least-once
		// delivery, so the same message can arrive twice, and a redelivery must
		// not post it again or bump the unread count again.
		res, err := l.db.Exec(`
			INSERT OR IGNORE INTO line_message
			  (line_message_id, line_user_id, direction, kind, body, provider_id, sent_at,
			   sticker_package_id, sticker_id, sticker_resource_type)
			VALUES (?, ?, 'In', ?, ?, ?, ?, ?, ?, ?)`,
			db.NewID("lmsg"), uid, lineKindOf(ev.Message.Type), ev.Message.Text, ev.Message.ID, at,
			db.NullIfEmpty(ev.Message.PackageID), db.NullIfEmpty(ev.Message.StickerID),
			db.NullIfEmpty(ev.Message.StickerResourceType))
		if err != nil {
			log.Printf("line: record inbound from %s: %v", uid, err)
			return false
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return false // already had it
		}
		if _, err := l.db.Exec(`UPDATE line_contact
		                        SET last_message_at = ?, unread_count = unread_count + 1,
		                            followed = 1, reply_token = ?, reply_token_at = ?
		                        WHERE line_user_id = ?`,
			at, ev.ReplyToken, db.SQLiteNow(), uid); err != nil {
			log.Printf("line: touch contact %s: %v", uid, err)
		}
		return true
	}
	return false
}

// upsertContact makes sure the thread exists, and fills in the person's name
// the first time we see them.
//
// The profile lookup is best-effort. It only works for someone who has added
// the account as a friend, so a failure is ordinary — and a message must be
// recorded whether or not we can put a name to it.
func (l *lineDeps) upsertContact(uid string, c lineSender) {
	var name string
	err := l.db.QueryRow(`SELECT display_name FROM line_contact WHERE line_user_id = ?`, uid).Scan(&name)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		now := db.SQLiteNow()
		if _, err := l.db.Exec(`INSERT INTO line_contact (line_user_id, first_seen_at, last_message_at)
		                        VALUES (?, ?, ?)`, uid, now, now); err != nil {
			log.Printf("line: create contact %s: %v", uid, err)
			return
		}
	case err != nil:
		log.Printf("line: read contact %s: %v", uid, err)
		return
	case name != "":
		return // already named
	}
	display, picture, err := c.Profile(uid)
	if err != nil {
		log.Printf("line: profile %s unavailable: %v", uid, err)
		return
	}
	if _, err := l.db.Exec(`UPDATE line_contact SET display_name = ?, picture_url = ?
	                        WHERE line_user_id = ?`, display, picture, uid); err != nil {
		log.Printf("line: name contact %s: %v", uid, err)
	}
}
