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
	"log"
	"os"
	"strings"

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
	// The JTrax record the office linked this chat to (0069): a parent or a
	// student, never both, and its name to show.
	ParentID   string `json:"parentId,omitempty"`
	StudentID  string `json:"studentId,omitempty"`
	LinkedName string `json:"linkedName,omitempty"`
}

// lineLinkCols reads a contact's link, from line_contact c joined as below.
const lineLinkCols = `COALESCE(c.parent_id, ''), COALESCE(c.student_id, ''), COALESCE(lp.name, ls.name, '')`

const lineLinkJoins = `
		LEFT JOIN parent lp ON lp.parent_id = c.parent_id
		LEFT JOIN student ls ON ls.student_id = c.student_id`

// lineSticker is how LINE names a sticker: which package, which sticker in it,
// and how it is drawn.
type lineSticker struct {
	PackageID    string `json:"packageId"`
	StickerID    string `json:"stickerId"`
	ResourceType string `json:"resourceType,omitempty"`
}

type lineMessageView struct {
	ID        string       `json:"id"`
	Direction string       `json:"direction"` // In | Out
	Kind      string       `json:"kind"`
	Body      string       `json:"body"`
	SentAt    string       `json:"sentAt"`
	SentBy    string       `json:"sentBy,omitempty"`  // staff display name
	Channel   string       `json:"channel,omitempty"` // reply | push
	Delivery  string       `json:"delivery"`          // Sent | Failed
	Reason    string       `json:"failureReason,omitempty"`
	Sticker   *lineSticker `json:"sticker,omitempty"`
}

func listConversations(d *sql.DB) ([]lineConversation, error) {
	rows, err := d.Query(`
		SELECT c.line_user_id, c.display_name, c.picture_url, c.followed,
		       c.last_message_at, c.unread_count,
		       COALESCE(m.body, ''), COALESCE(m.kind, ''), COALESCE(m.direction, ''),
		       ` + lineLinkCols + `
		FROM line_contact c` + lineLinkJoins + `
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
			&c.LastAt, &c.Unread, &c.Preview, &c.PreviewKind, &c.PreviewFrom,
			&c.ParentID, &c.StudentID, &c.LinkedName); err != nil {
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
		       m.channel_used, m.delivery, m.failure_reason,
		       COALESCE(m.sticker_package_id, ''), COALESCE(m.sticker_id, ''),
		       COALESCE(m.sticker_resource_type, '')
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
		var st lineSticker
		if err := rows.Scan(&m.ID, &m.Direction, &m.Kind, &m.Body, &m.SentAt,
			&m.SentBy, &m.Channel, &m.Delivery, &m.Reason,
			&st.PackageID, &st.StickerID, &st.ResourceType); err != nil {
			return nil, err
		}
		if st.StickerID != "" {
			m.Sticker = &st
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

func mountLine(mux routeRegistrar, d *sql.DB) {
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
	mux.HandleFunc("PUT "+p+"/conversations/{id}/link", handleLineLink(deps))
	mux.HandleFunc("GET "+p+"/events", handleLineEvents(deps))

	mux.HandleFunc("GET "+p+"/channel", handleLineChannelGet(deps))
	mux.HandleFunc("PUT "+p+"/channel", handleLineChannelPut(deps))
	mux.HandleFunc("DELETE "+p+"/channel", handleLineChannelDelete(deps))
}
