// Staff inbox operations and live LINE conversation updates.
package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Kusk24/jtrax-backend/internal/httpx"
	"github.com/Kusk24/jtrax-backend/internal/line"
)

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
		c, err := conversationOf(l.db, uid)
		if errors.Is(err, sql.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, "not found", nil)
			return
		}
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not load conversation", err)
			return
		}
		messages, err := threadOf(l.db, uid)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not load messages", err)
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"conversation": c, "messages": messages})
	}
}

// conversationOf is one contact's header: who, whether they follow, and the
// record they are linked to. Its preview fields are left empty.
func conversationOf(d *sql.DB, uid string) (lineConversation, error) {
	var c lineConversation
	var followed int
	err := d.QueryRow(`SELECT c.line_user_id, c.display_name, c.picture_url, c.followed,
	                          c.last_message_at, c.unread_count, `+lineLinkCols+`
	                   FROM line_contact c`+lineLinkJoins+`
	                   WHERE c.line_user_id = ?`, uid).
		Scan(&c.UserID, &c.DisplayName, &c.PictureURL, &followed, &c.LastAt, &c.Unread,
			&c.ParentID, &c.StudentID, &c.LinkedName)
	c.Followed = followed == 1
	c.LastAt = sqliteISO(c.LastAt)
	return c, err
}

// handleLineLink links a chat to a parent or a student, or — with neither —
// unlinks it. A record already linked to another chat is refused rather than
// moved, so one click cannot quietly take a family's chat from them.
func handleLineLink(l *lineDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if requireStaff(l.db, w, r) == nil {
			return
		}
		uid := r.PathValue("id")
		var in struct {
			ParentID  string `json:"parentId"`
			StudentID string `json:"studentId"`
		}
		if err := httpx.Decode(r, &in); err != nil {
			httpx.Error(w, http.StatusBadRequest, "malformed request", err)
			return
		}
		in.ParentID, in.StudentID = strings.TrimSpace(in.ParentID), strings.TrimSpace(in.StudentID)
		if in.ParentID != "" && in.StudentID != "" {
			httpx.Error(w, http.StatusBadRequest, "link a parent or a student, not both", nil)
			return
		}
		if _, err := conversationOf(l.db, uid); errors.Is(err, sql.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, "not found", nil)
			return
		} else if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not load conversation", err)
			return
		}
		for _, target := range []struct{ id, table, col, missing string }{
			{in.ParentID, "parent", "parent_id", "no such parent"},
			{in.StudentID, "student", "student_id", "no such student"},
		} {
			if target.id == "" {
				continue
			}
			var n int
			l.db.QueryRow(`SELECT COUNT(*) FROM `+target.table+` WHERE `+target.col+` = ?`, target.id).Scan(&n)
			if n == 0 {
				httpx.Error(w, http.StatusNotFound, target.missing, nil)
				return
			}
			var other string
			err := l.db.QueryRow(`SELECT display_name FROM line_contact WHERE `+target.col+` = ? AND line_user_id <> ?`,
				target.id, uid).Scan(&other)
			if err == nil {
				httpx.Error(w, http.StatusConflict, "already linked to another LINE chat ("+other+") — unlink that one first", nil)
				return
			}
		}
		if _, err := l.db.Exec(`UPDATE line_contact SET parent_id = ?, student_id = ? WHERE line_user_id = ?`,
			nullIfEmpty(in.ParentID), nullIfEmpty(in.StudentID), uid); err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not link the chat", err)
			return
		}
		c, err := conversationOf(l.db, uid)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not load conversation", err)
			return
		}
		l.publishInbox(uid)
		httpx.JSON(w, http.StatusOK, c)
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

		// Send the current list at once. Until something is written the
		// headers are not sent either, so the console saw no stream for a whole
		// heartbeat on every open and called it disconnected. It also puts right
		// anything that arrived while a client was reconnecting.
		if list, err := listConversations(l.db); err == nil {
			if payload, err := json.Marshal(map[string]any{"conversations": list, "changed": ""}); err == nil {
				fmt.Fprintf(w, "event: inbox\ndata: %s\n\n", payload)
			}
		}
		flusher.Flush()

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
