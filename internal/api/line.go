// LINE Official Account inbox: the webhook LINE posts to, and the endpoints
// the console reads and replies through.
//
// Hand-written rather than declared in the registry because none of it is
// CRUD. The webhook is authenticated by a signature instead of a session, a
// conversation is a contact joined to its latest message rather than a row, and
// sending picks between two transports with different costs.
//
// Only staff reach the inbox. Teachers are not staff here — the academy's LINE
// account is the front desk's, and a teacher answering as the school is a
// different feature with a different authorization story.
package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Kusk24/jtrax-backend/internal/httpx"
	"github.com/Kusk24/jtrax-backend/internal/line"
	"github.com/Kusk24/jtrax-backend/internal/secretbox"
)

// lineKeyVar names the environment variable holding the sealing key for the
// stored channel credentials.
const lineKeyVar = "LINE_TOKEN_KEY"

// lineMaxWebhookBytes caps an inbound webhook body. LINE batches events, but a
// realistic batch is kilobytes; this endpoint is unauthenticated until the
// signature is checked, and the signature cannot be checked without first
// holding the whole body in memory.
const lineMaxWebhookBytes = 1 << 20

// lineInboxKey is the hub topic. There is one inbox, so unlike a game room the
// topic is a constant.
const lineInboxKey = "line-inbox"

// lineThreadLimit is how much of a conversation the console loads. Threads with
// a parent run for months; the screen shows the recent end of one.
const lineThreadLimit = 200

// lineSender is what the handlers need from the LINE API. An interface so a
// test can drive the whole flow — webhook in, reply out — without a network,
// and so the send path's fallback logic is testable at all.
type lineSender interface {
	Reply(replyToken, text string) error
	Push(userID, text string) error
	Profile(userID string) (name, picture string, err error)
	Quota() (limit, used int64, limited bool, err error)
}

type lineDeps struct {
	db  *sql.DB
	hub *hub
	// box is nil when the environment has no sealing key. That is not an error
	// at boot — an academy that does not use LINE should not have to set one —
	// but it makes storing credentials refuse rather than fall back to clear.
	box       *secretbox.Box
	newClient func(token string) lineSender
}

var (
	errLineNoKey         = errors.New("no sealing key configured")
	errLineNotConfigured = errors.New("no channel credentials stored")
)

type lineCreds struct{ Token, Secret string }

// creds decrypts the stored channel credentials.
func (l *lineDeps) creds() (*lineCreds, error) {
	if l.box == nil {
		return nil, errLineNoKey
	}
	var tokEnc, secEnc string
	err := l.db.QueryRow(`SELECT access_token_enc, channel_secret_enc FROM line_channel
	                      WHERE channel_row_id = 'default'`).Scan(&tokEnc, &secEnc)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errLineNotConfigured
	}
	if err != nil {
		return nil, err
	}
	token, err := l.box.Open(tokEnc)
	if err != nil {
		return nil, err
	}
	secret, err := l.box.Open(secEnc)
	if err != nil {
		return nil, err
	}
	return &lineCreds{Token: token, Secret: secret}, nil
}

/* ---- views ---- */

type lineConversation struct {
	UserID      string `json:"lineUserId"`
	DisplayName string `json:"displayName"`
	PictureURL  string `json:"pictureUrl,omitempty"`
	Followed    bool   `json:"followed"`
	LastAt      string `json:"lastMessageAt"`
	Unread      int    `json:"unread"`
	Preview     string `json:"preview"`
	PreviewKind string `json:"previewKind,omitempty"`
	PreviewFrom string `json:"previewFrom,omitempty"` // In | Out
}

type lineMessageView struct {
	ID        string `json:"id"`
	Direction string `json:"direction"` // In | Out
	Kind      string `json:"kind"`
	Body      string `json:"body"`
	SentAt    string `json:"sentAt"`
	SentBy    string `json:"sentBy,omitempty"`  // staff display name
	Channel   string `json:"channel,omitempty"` // reply | push
	Delivery  string `json:"delivery"`          // Sent | Failed
	Reason    string `json:"failureReason,omitempty"`
}

func listConversations(d *sql.DB) ([]lineConversation, error) {
	rows, err := d.Query(`
		SELECT c.line_user_id, c.display_name, c.picture_url, c.followed,
		       c.last_message_at, c.unread_count,
		       COALESCE(m.body, ''), COALESCE(m.kind, ''), COALESCE(m.direction, '')
		FROM line_contact c
		LEFT JOIN line_message m ON m.line_message_id = (
		    SELECT m2.line_message_id FROM line_message m2
		    WHERE m2.line_user_id = c.line_user_id
		    ORDER BY m2.sent_at DESC, m2.rowid DESC LIMIT 1)
		ORDER BY c.last_message_at DESC, c.line_user_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []lineConversation{}
	for rows.Next() {
		var c lineConversation
		var followed int
		if err := rows.Scan(&c.UserID, &c.DisplayName, &c.PictureURL, &followed,
			&c.LastAt, &c.Unread, &c.Preview, &c.PreviewKind, &c.PreviewFrom); err != nil {
			return nil, err
		}
		c.Followed = followed == 1
		c.LastAt = sqliteISO(c.LastAt)
		out = append(out, c)
	}
	return out, rows.Err()
}

func threadOf(d *sql.DB, userID string) ([]lineMessageView, error) {
	// Newest first with a limit, then reversed: the console shows the recent
	// end of a long conversation, and SQLite cannot take the last N directly.
	rows, err := d.Query(`
		SELECT m.line_message_id, m.direction, m.kind, m.body, m.sent_at,
		       COALESCE((SELECT ua.display_name FROM user_account ua
		                 WHERE ua.user_account_id = m.sent_by), ''),
		       m.channel_used, m.delivery, m.failure_reason
		FROM line_message m
		WHERE m.line_user_id = ?
		ORDER BY m.sent_at DESC, m.rowid DESC
		LIMIT ?`, userID, lineThreadLimit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []lineMessageView{}
	for rows.Next() {
		var m lineMessageView
		if err := rows.Scan(&m.ID, &m.Direction, &m.Kind, &m.Body, &m.SentAt,
			&m.SentBy, &m.Channel, &m.Delivery, &m.Reason); err != nil {
			return nil, err
		}
		m.SentAt = sqliteISO(m.SentAt)
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

// publishInbox fans out the conversation list. Like the game hub it sends a
// full snapshot rather than a delta, so a console that missed an event still
// converges and a reconnect needs no replay.
//
// `changed` names the thread that moved, so a console with a thread open knows
// whether the read it is showing is now stale — and, more to the point, knows
// when it is *not*, which is what stops every message in the academy causing
// every open console to refetch.
func (l *lineDeps) publishInbox(changed string) {
	list, err := listConversations(l.db)
	if err != nil {
		log.Printf("line: publish inbox: %v", err)
		return
	}
	payload, err := json.Marshal(map[string]any{"conversations": list, "changed": changed})
	if err != nil {
		return
	}
	l.hub.broadcast(lineInboxKey, payload)
}

/* ---- webhook ---- */

// lineKindOf maps an event's message type onto the kinds the schema allows.
// Anything unrecognised is recorded as 'other' rather than dropped: a gap in a
// thread reads as a bug, and staff still need to see that something arrived.
func lineKindOf(t string) string {
	switch t {
	case "text", "sticker", "image", "video", "audio", "file", "location":
		return t
	default:
		return "other"
	}
}

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
		                        WHERE line_user_id = ?`, ev.ReplyToken, sqliteNow(), uid); err != nil {
			log.Printf("line: follow %s: %v", uid, err)
			return false
		}
		return true

	case "message":
		l.upsertContact(uid, c)
		at := ev.At().Format(sqliteTimeLayout)
		// OR IGNORE against the unique provider id: LINE guarantees at-least-once
		// delivery, so the same message can arrive twice, and a redelivery must
		// not post it again or bump the unread count again.
		res, err := l.db.Exec(`
			INSERT OR IGNORE INTO line_message
			  (line_message_id, line_user_id, direction, kind, body, provider_id, sent_at)
			VALUES (?, ?, 'In', ?, ?, ?, ?)`,
			newID("lmsg"), uid, lineKindOf(ev.Message.Type), ev.Message.Text, ev.Message.ID, at)
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
			at, ev.ReplyToken, sqliteNow(), uid); err != nil {
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
		now := sqliteNow()
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

/* ---- inbox ---- */

func handleLineConversations(l *lineDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if requireStaff(l.db, w, r) == nil {
			return
		}
		list, err := listConversations(l.db)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not load conversations", err)
			return
		}
		httpx.JSON(w, http.StatusOK, list)
	}
}

func handleLineThread(l *lineDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if requireStaff(l.db, w, r) == nil {
			return
		}
		uid := r.PathValue("id")
		var c lineConversation
		var followed int
		err := l.db.QueryRow(`SELECT line_user_id, display_name, picture_url, followed,
		                             last_message_at, unread_count
		                      FROM line_contact WHERE line_user_id = ?`, uid).
			Scan(&c.UserID, &c.DisplayName, &c.PictureURL, &followed, &c.LastAt, &c.Unread)
		if errors.Is(err, sql.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, "not found", nil)
			return
		}
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not load conversation", err)
			return
		}
		c.Followed = followed == 1
		c.LastAt = sqliteISO(c.LastAt)
		messages, err := threadOf(l.db, uid)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not load messages", err)
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"conversation": c, "messages": messages})
	}
}

func handleLineMarkRead(l *lineDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if requireStaff(l.db, w, r) == nil {
			return
		}
		uid := r.PathValue("id")
		res, err := l.db.Exec(`UPDATE line_contact SET unread_count = 0 WHERE line_user_id = ?`, uid)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not update conversation", err)
			return
		}
		if n, _ := res.RowsAffected(); n == 0 {
			httpx.Error(w, http.StatusNotFound, "not found", nil)
			return
		}
		l.publishInbox(uid)
		httpx.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

func handleLineSend(l *lineDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := requireIdentity(l.db, w, r)
		if id == nil {
			return
		}
		if !isStaff(id.Role) {
			httpx.Error(w, http.StatusForbidden, "not allowed", nil)
			return
		}
		var in struct {
			Text string `json:"text"`
		}
		if err := httpx.Decode(r, &in); err != nil {
			httpx.Error(w, http.StatusBadRequest, "text is required", err)
			return
		}
		text := strings.TrimSpace(in.Text)
		if text == "" {
			httpx.Error(w, http.StatusBadRequest, "text is required", nil)
			return
		}
		if utf8.RuneCountInString(text) > line.MaxTextLength {
			httpx.Error(w, http.StatusBadRequest,
				fmt.Sprintf("a message may be at most %d characters", line.MaxTextLength), nil)
			return
		}

		uid := r.PathValue("id")
		var followed int
		var replyToken sql.NullString
		var replyAt sql.NullString
		err := l.db.QueryRow(`SELECT followed, reply_token, reply_token_at
		                      FROM line_contact WHERE line_user_id = ?`, uid).
			Scan(&followed, &replyToken, &replyAt)
		if errors.Is(err, sql.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, "not found", nil)
			return
		}
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not load conversation", err)
			return
		}
		if followed == 0 {
			// Nothing to attempt: LINE rejects a send to someone who has
			// blocked the account, and spending the attempt to find that out
			// would record a failure the console already knows about.
			httpx.JSON(w, http.StatusConflict, map[string]string{
				"error": "this contact has blocked the account", "reason": string(line.ReasonBlocked)})
			return
		}
		creds, err := l.creds()
		if err != nil {
			httpx.Error(w, http.StatusServiceUnavailable, "line messaging is not configured", err)
			return
		}
		client := l.newClient(creds.Token)

		used, sendErr := l.deliver(client, uid, text, replyToken, replyAt)

		delivery, reason := "Sent", ""
		if sendErr != nil {
			delivery = "Failed"
			reason = string(line.ReasonOf(sendErr))
			log.Printf("line: send to %s failed: %v", uid, sendErr)
		}
		msgID := newID("lmsg")
		at := sqliteNow()
		if _, err := l.db.Exec(`
			INSERT INTO line_message
			  (line_message_id, line_user_id, direction, kind, body, sent_at, sent_by,
			   channel_used, delivery, failure_reason)
			VALUES (?, ?, 'Out', 'text', ?, ?, ?, ?, ?, ?)`,
			msgID, uid, text, at, id.UserAccountID, used, delivery, reason); err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not record message", err)
			return
		}
		// A failed send is recorded and shown too. "Did she get my message?" is
		// the question this screen exists to answer, and a message that quietly
		// vanished answers it wrongly.
		if _, err := l.db.Exec(`UPDATE line_contact SET last_message_at = ?, unread_count = 0
		                        WHERE line_user_id = ?`, at, uid); err != nil {
			log.Printf("line: touch contact %s: %v", uid, err)
		}
		l.publishInbox(uid)

		view := lineMessageView{
			ID: msgID, Direction: "Out", Kind: "text", Body: text,
			SentAt: sqliteISO(at), SentBy: id.DisplayName,
			Channel: used, Delivery: delivery, Reason: reason,
		}
		if sendErr != nil {
			httpx.JSON(w, http.StatusBadGateway, map[string]any{
				"error": "message could not be delivered", "reason": reason, "message": view})
			return
		}
		httpx.JSON(w, http.StatusOK, view)
	}
}

// deliver sends the text, preferring a live reply token over a metered push.
//
// The fallback is the point: a reply token is free but short-lived, and the
// only certain way to know whether one is still good is to spend it. Trying the
// free path first and falling back costs one wasted call when it has expired;
// not trying costs a billed message every time.
func (l *lineDeps) deliver(c lineSender, uid, text string, token, tokenAt sql.NullString) (used string, err error) {
	if token.Valid && token.String != "" && lineTokenFresh(tokenAt) {
		// Single use, so it is spent whatever happens next.
		if _, err := l.db.Exec(`UPDATE line_contact SET reply_token = NULL, reply_token_at = NULL
		                        WHERE line_user_id = ?`, uid); err != nil {
			log.Printf("line: clear reply token for %s: %v", uid, err)
		}
		if err := c.Reply(token.String, text); err == nil {
			return "reply", nil
		} else {
			log.Printf("line: reply token for %s not accepted, falling back to push: %v", uid, err)
		}
	}
	return "push", c.Push(uid, text)
}

func lineTokenFresh(at sql.NullString) bool {
	if !at.Valid || at.String == "" {
		return false
	}
	t, err := time.Parse(sqliteTimeLayout, at.String)
	if err != nil {
		return false
	}
	return time.Since(t.UTC()) < line.ReplyTokenTTL
}

// handleLineEvents streams the inbox to the console.
func handleLineEvents(l *lineDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if requireStaff(l.db, w, r) == nil {
			return
		}
		flusher, ok := w.(http.Flusher)
		if !ok {
			httpx.Error(w, http.StatusInternalServerError, "streaming unavailable", nil)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)

		ch := l.hub.subscribe(lineInboxKey)
		defer l.hub.unsubscribe(lineInboxKey, ch)

		ticker := time.NewTicker(heartbeat)
		defer ticker.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case payload := <-ch:
				fmt.Fprintf(w, "event: inbox\ndata: %s\n\n", payload)
				flusher.Flush()
			case <-ticker.C:
				fmt.Fprint(w, ": ping\n\n")
				flusher.Flush()
			}
		}
	}
}

/* ---- channel credentials ----

   Admin only, and tighter than the rest of the console: a receptionist answers
   messages but does not hold the credential that sends them. The stored token
   is never returned by any endpoint — the console shows the last four
   characters, which is enough to tell two credentials apart and not enough to
   use one. */

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

// lineWebhookURL is the address an operator pastes into the LINE console. It is
// derived from the request rather than configured, because it is by definition
// the address this API was just reached on.
func lineWebhookURL(r *http.Request) string {
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

func mountLine(mux *http.ServeMux, d *sql.DB) {
	box, err := secretbox.FromEnv(lineKeyVar)
	if err != nil && !errors.Is(err, secretbox.ErrNoKey) {
		// A malformed key is worth shouting about: the operator meant to
		// configure this and it will fail at the first save with no other clue.
		log.Printf("line: %v — messaging credentials cannot be stored", err)
	}
	// LINE_API_BASE exists so a test can point the client at a stub, and so a
	// deployment behind an egress proxy can be pointed at it. Unset in normal
	// use, where the client's own default applies.
	base := strings.TrimSpace(os.Getenv("LINE_API_BASE"))
	deps := &lineDeps{
		db:  d,
		hub: newHub(),
		box: box,
		newClient: func(token string) lineSender {
			c := line.New(token)
			if base != "" {
				c.BaseURL = base
			}
			return c
		},
	}

	const p = "/api/v1/line"
	// The webhook is the one unauthenticated endpoint here. It is authenticated
	// by signature rather than session, but the rate limit still applies:
	// verification happens after the body is read, so an unsigned flood would
	// otherwise be free work. The budget is generous because a real batch of
	// events from a busy account is legitimate traffic.
	mux.HandleFunc("POST "+p+"/webhook", httpx.RateLimit(120, handleLineWebhook(deps)))

	mux.HandleFunc("GET "+p+"/conversations", handleLineConversations(deps))
	mux.HandleFunc("GET "+p+"/conversations/{id}", handleLineThread(deps))
	mux.HandleFunc("POST "+p+"/conversations/{id}/messages", handleLineSend(deps))
	mux.HandleFunc("POST "+p+"/conversations/{id}/read", handleLineMarkRead(deps))
	mux.HandleFunc("GET "+p+"/events", handleLineEvents(deps))

	mux.HandleFunc("GET "+p+"/channel", handleLineChannelGet(deps))
	mux.HandleFunc("PUT "+p+"/channel", handleLineChannelPut(deps))
	mux.HandleFunc("DELETE "+p+"/channel", handleLineChannelDelete(deps))
}
