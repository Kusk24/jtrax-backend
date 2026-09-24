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
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/Kusk24/jtrax-backend/internal/mail"
	"github.com/Kusk24/jtrax-backend/internal/push"
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
	TypeCheckIn        = "check_in"
	TypeCheckOut       = "check_out"
	TypeCreditDeducted = "credit_deducted"
	TypeLowCredit      = "low_credit"
	TypeCreditExpiry   = "credit_expiry"
	TypeAnnouncement   = "announcement"
	TypePayment        = "payment_received"
	// A class called off by the office, to the families who were due at it.
	TypeClassCancelled = "class_cancelled"
)

// DefaultEnabled says whether a person receives this type without ever having
// touched their settings. Everything defaults on. The low-credit nudge used to
// be opt-in, back when it fired by itself at every check-out; since 2026-09-24
// staff send it by hand, like the expiry reminder, and a reminder the desk
// chose to send should reach the family unless they switched it off.
func DefaultEnabled(typ string) bool { return true }

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
	// push delivers to phones. Nil leaves phone deliveries pending, which is
	// what they were before there was a sender.
	push Pusher
}

// Pusher hands phone notifications to a push service and says, per message,
// whether each reached it. *push.Client is the real one.
type Pusher interface {
	Send(ctx context.Context, msgs []push.Message) []push.Result
}

// SetPush turns phone delivery on.
func (s *Service) SetPush(p Pusher) { s.push = p }

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
//
// Two gates run before anything is written. The academy's own switch: the
// admin can turn a whole type off for the school, and a switched-off type
// sends nobody anything. Then each recipient's: the in-app row for a type is
// that person's master toggle, so someone who turned "check-in alerts" off —
// or never turned an opt-in type on — is skipped entirely, not sent a
// notification their settings say they did not want.
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

	if !typeEnabledForSchool(tx, msg.Type) {
		return nil
	}

	type emailJob struct{ deliveryID, notifID, addr, subject, body string }
	var emails []emailJob
	var phones []pushJob

	for _, uid := range recipients {
		if !prefEnabled(tx, uid, msg.Type, ChannelInApp) {
			continue
		}
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
				// Already through the recipient gate above, so it lands.
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
				} else if ch == ChannelMobile && s.push != nil {
					// Sent after the commit, like email; the row stays
					// 'pending' until Expo has answered.
					phones = append(phones, pushJob{deliveryID, uid, notifID, msg.Type, title, body, msg.Data})
				}
				// Browser push has no sender yet, so it stays 'pending'.
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
	s.sendToPhones(phones)
	return nil
}

// pushJob is one notification waiting to go to one person's phones.
type pushJob struct {
	deliveryID, uid, notifID, typ, title, body string
	data                                       map[string]any
}

// sendToPhones delivers each job to every phone its person has registered, in
// one call to the push service. A delivery is 'sent' if it reached at least
// one of that person's phones. A phone the service says is no longer
// registered (the app was removed) is marked failed, so it is not tried again.
func (s *Service) sendToPhones(jobs []pushJob) {
	if len(jobs) == 0 || s.push == nil {
		return
	}
	type target struct {
		job            int
		subscriptionID string
	}
	var msgs []push.Message
	var targets []target
	for i, j := range jobs {
		rows, err := s.db.Query(
			`SELECT push_subscription_id, endpoint FROM push_subscription
			  WHERE user_account_id = ? AND channel = ? AND failed_at IS NULL`, j.uid, ChannelMobile)
		if err != nil {
			continue
		}
		for rows.Next() {
			var subID, token string
			if rows.Scan(&subID, &token) != nil || !push.IsExpoToken(token) {
				continue
			}
			data := map[string]any{"notificationId": j.notifID, "type": j.typ}
			for k, v := range j.data {
				data[k] = v
			}
			msgs = append(msgs, push.Message{To: token, Title: j.title, Body: j.body, Data: data})
			targets = append(targets, target{i, subID})
		}
		rows.Close()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	results := s.push.Send(ctx, msgs)

	reached := make([]bool, len(jobs))
	reason := make([]string, len(jobs))
	for n, r := range results {
		if n >= len(targets) {
			break
		}
		t := targets[n]
		if r.OK {
			reached[t.job] = true
			continue
		}
		reason[t.job] = r.Error
		if r.Unregistered {
			s.db.Exec(`UPDATE push_subscription SET failed_at = datetime('now')
			            WHERE push_subscription_id = ?`, t.subscriptionID)
		}
	}
	for i, j := range jobs {
		switch {
		case reached[i]:
			s.markDelivery(j.deliveryID, "sent", "")
		case reason[i] != "":
			s.markDelivery(j.deliveryID, "failed", reason[i])
		default:
			// Registered, but no token this sender can reach.
			s.markDelivery(j.deliveryID, "failed", "no Expo push token")
		}
	}
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

// Email sends one message straight to an address that has no account behind
// it — a public tournament entrant, say — so there is no inbox or preference to
// go through. A deployment with no mail configured sends nothing; a failure is
// logged with the address redacted and never returned, because the caller's
// work is already done.
func (s *Service) Email(to, subject, body string) {
	// mail.New returns no sender at all when SMTP is not configured, so a
	// sender being here is what "mail is on" means.
	if s.mail == nil || to == "" {
		return
	}
	if err := s.mail.Send(to, subject, body); err != nil {
		log.Printf("notify: email to %s failed: %v", redactAddr(to), err)
	}
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

// prefEnabled reads the per-user override for (type, channel); an absent row
// means the type's default — on for everything except the opt-in types (see
// DefaultEnabled). Only explicit choices are ever stored, so silence means
// "whatever the default says".
func prefEnabled(tx *sql.Tx, uid, typ, channel string) bool {
	var enabled int
	err := tx.QueryRow(
		`SELECT enabled FROM notification_setting WHERE user_account_id = ? AND type = ? AND channel = ?`,
		uid, typ, channel).Scan(&enabled)
	if err == sql.ErrNoRows {
		return DefaultEnabled(typ)
	}
	if err != nil {
		return DefaultEnabled(typ)
	}
	return enabled != 0
}

// typeEnabledForSchool is the admin's switch: `notify_<type>` in
// system_configuration, and only an explicit "off" turns a type off — an
// absent key is on, so a new type works before anyone visits Settings.
func typeEnabledForSchool(tx *sql.Tx, typ string) bool {
	var value string
	err := tx.QueryRow(
		`SELECT config_value FROM system_configuration WHERE config_key = ?`,
		"notify_"+typ).Scan(&value)
	if err != nil {
		return true
	}
	return value != "off"
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
