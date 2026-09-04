// Package notify turns things that happen — a check-in, an announcement, a
// credit about to expire — into notifications a person actually receives.
//
// The in-app inbox is the backbone: every notification lands there regardless
// of preferences, so nothing is ever silently lost. Email, browser push and
// mobile push are additional channels a recipient can switch off per type. The
// event *catalogue* (which types exist) lives here as constants; the academy
// supplies which events matter, not how they are delivered.
//
// Single-writer note: modernc sqlite runs with one connection, so the inbox
// rows are written in one transaction and the slow channels (email, push) are
// attempted after it commits — a fan-out must never hold the write lock across
// a network call.
package notify

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"

	"github.com/Kusk24/jtrax-backend/internal/mail"
)

// Channels a notification can go out over.
const (
	ChannelInApp   = "inapp"
	ChannelEmail   = "email"
	ChannelWebPush = "webpush"
	ChannelMobile  = "mobile"
)

// Channels is the full set, in the order deliveries are recorded.
var Channels = []string{ChannelInApp, ChannelEmail, ChannelWebPush, ChannelMobile}

// Event types. The academy will extend this list; the schema stores type as a
// string precisely so new ones need no migration.
const (
	TypeCheckIn      = "check_in"
	TypeCheckOut     = "check_out"
	TypeCreditExpiry = "credit_expiry"
	TypeAnnouncement = "announcement"
)

// Text is one string in both supported languages. The sender picks per
// recipient from user_account.language_preference, so a family that reads Thai
// and a coach that reads English each get their own.
type Text struct{ EN, TH string }

func (t Text) pick(lang string) string {
	if lang == "th" && t.TH != "" {
		return t.TH
	}
	return t.EN
}

// Message is one event, before it is fanned out to recipients.
type Message struct {
	Type  string
	Title Text
	Body  Text
	// Data is merged into each notification's JSON payload for deep links, and
	// carries the dedupe key when set (see DedupeKey).
	Data map[string]any
	// DedupeKey, when set, skips any recipient who already has a notification
	// of this Type whose data.dedupe matches — so a check-in row patched twice
	// notifies once. Empty means every send is delivered.
	DedupeKey string
}

// Service sends notifications. It holds the mail sender so the email channel
// works the moment SMTP is configured, and is inert (deliveries recorded
// 'pending') until then.
type Service struct {
	db      *sql.DB
	mail    mail.Sender
	mailCfg mail.Config
}

func New(db *sql.DB, sender mail.Sender, cfg mail.Config) *Service {
	return &Service{db: db, mail: sender, mailCfg: cfg}
}

func newID(prefix string) string {
	raw := make([]byte, 5)
	rand.Read(raw)
	return prefix + "_" + hex.EncodeToString(raw)
}

// Send fans a message out to recipients (user_account_ids). The inbox rows are
// written and committed first; email and push are attempted afterwards. It
// returns an error only if the inbox write itself failed — a failed email is
// recorded on the delivery row, not bubbled up, because the event still
// happened and the recipient can still see it in-app.
func (s *Service) Send(recipients []string, msg Message) error {
	if len(recipients) == 0 {
		return nil
	}
	dataJSON := s.encodeData(msg)

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	type emailJob struct{ deliveryID, notifID, addr, subject, body string }
	var emails []emailJob

	for _, uid := range recipients {
		if msg.DedupeKey != "" && s.alreadySent(tx, uid, msg.Type, msg.DedupeKey) {
			continue
		}
		lang := lookupLanguage(tx, uid)
		title := msg.Title.pick(lang)
		body := msg.Body.pick(lang)

		notifID := newID("ntf")
		if _, err := tx.Exec(
			`INSERT INTO notification (notification_id, user_account_id, type, title, body, data)
			 VALUES (?,?,?,?,?,?)`,
			notifID, uid, msg.Type, title, body, dataJSON); err != nil {
			return err
		}

		for _, ch := range Channels {
			deliveryID := newID("ndl")
			status := "pending"
			switch {
			case ch == ChannelInApp:
				// The inbox always receives it; that is the point of an inbox.
				status = "sent"
			case !prefEnabled(tx, uid, msg.Type, ch):
				status = "skipped_by_preference"
			case ch == ChannelEmail:
				addr := lookupEmail(tx, uid)
				if s.mailCfg.Configured() && addr != "" {
					// Queue the actual send for after commit; the row stays
					// 'pending' until that succeeds or fails.
					emails = append(emails, emailJob{deliveryID, notifID, addr, title, body})
				}
				// No SMTP yet, or no address: left 'pending' so a sender that
				// runs later can pick it up, not 'failed'.
			case ch == ChannelWebPush || ch == ChannelMobile:
				if !hasSubscription(tx, uid, ch) {
					status = "skipped_by_preference"
				}
				// else 'pending': a push worker delivers it out of band. The
				// VAPID/Expo send path is deliberately not inlined here.
			}
			if err := insertDelivery(tx, deliveryID, notifID, ch, status); err != nil {
				return err
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return err
	}

	// After the lock is released: attempt the emails and record the outcome.
	for _, j := range emails {
		if err := s.mail.Send(j.addr, j.subject, j.body); err != nil {
			log.Printf("notify: email to %s failed: %v", redactAddr(j.addr), err)
			s.markDelivery(j.deliveryID, "failed", err.Error())
			continue
		}
		s.markDelivery(j.deliveryID, "sent", "")
	}
	return nil
}

func (s *Service) encodeData(msg Message) any {
	data := map[string]any{}
	for k, v := range msg.Data {
		data[k] = v
	}
	if msg.DedupeKey != "" {
		data["dedupe"] = msg.DedupeKey
	}
	if len(data) == 0 {
		return nil
	}
	b, err := json.Marshal(data)
	if err != nil {
		return nil
	}
	return string(b)
}

// alreadySent reports whether this recipient already holds a notification of
// this type with the same dedupe key. Uses json_extract, which modernc sqlite
// supports, so it reads the key straight out of the stored payload.
func (s *Service) alreadySent(tx *sql.Tx, uid, typ, key string) bool {
	var n int
	tx.QueryRow(
		`SELECT COUNT(*) FROM notification
		 WHERE user_account_id = ? AND type = ? AND json_extract(data,'$.dedupe') = ?`,
		uid, typ, key).Scan(&n)
	return n > 0
}

func (s *Service) markDelivery(id, status, errText string) {
	var errCol any
	if errText != "" {
		errCol = errText
	}
	if _, err := s.db.Exec(
		`UPDATE notification_delivery SET status = ?, sent_at = datetime('now'), error = ?
		 WHERE notification_delivery_id = ?`, status, errCol, id); err != nil {
		log.Printf("notify: could not record delivery %s: %v", id, err)
	}
}

func insertDelivery(tx *sql.Tx, id, notifID, channel, status string) error {
	var sentAt any // NULL unless it went out immediately (inapp).
	if status == "sent" {
		sentAt = sqlNow(tx)
	}
	_, err := tx.Exec(
		`INSERT INTO notification_delivery (notification_delivery_id, notification_id, channel, status, sent_at)
		 VALUES (?,?,?,?,?)`,
		id, notifID, channel, status, sentAt)
	return err
}

func sqlNow(tx *sql.Tx) string {
	var now string
	tx.QueryRow(`SELECT datetime('now')`).Scan(&now)
	return now
}

// prefEnabled reads the per-user override for (type, channel); an absent row is
// the default, which is on. Only choices that differ from the default are ever
// stored, so silence means "yes".
func prefEnabled(tx *sql.Tx, uid, typ, channel string) bool {
	var enabled int
	err := tx.QueryRow(
		`SELECT enabled FROM notification_setting WHERE user_account_id = ? AND type = ? AND channel = ?`,
		uid, typ, channel).Scan(&enabled)
	if err == sql.ErrNoRows {
		return true
	}
	if err != nil {
		return true
	}
	return enabled != 0
}

func hasSubscription(tx *sql.Tx, uid, channel string) bool {
	var n int
	tx.QueryRow(
		`SELECT COUNT(*) FROM push_subscription WHERE user_account_id = ? AND channel = ? AND failed_at IS NULL`,
		uid, channel).Scan(&n)
	return n > 0
}

func lookupLanguage(tx *sql.Tx, uid string) string {
	var lang sql.NullString
	tx.QueryRow(`SELECT language_preference FROM user_account WHERE user_account_id = ?`, uid).Scan(&lang)
	if lang.Valid {
		return lang.String
	}
	return "en"
}

func lookupEmail(tx *sql.Tx, uid string) string {
	var email sql.NullString
	tx.QueryRow(`SELECT email FROM user_account WHERE user_account_id = ?`, uid).Scan(&email)
	if email.Valid {
		return email.String
	}
	return ""
}

// redactAddr keeps a failed-email log line useful without writing a full
// address into the logs.
func redactAddr(addr string) string {
	for i, c := range addr {
		if c == '@' {
			if i <= 1 {
				return "*" + addr[i:]
			}
			return addr[:1] + fmt.Sprintf("***%s", addr[i:])
		}
	}
	return "***"
}
