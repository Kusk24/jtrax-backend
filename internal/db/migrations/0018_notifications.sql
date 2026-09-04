-- 0018_notifications.sql — the notification system: an in-app inbox plus the
-- per-user, per-channel preferences and push subscriptions that feed it.
--
-- Supersedes the parent-only notification_preference table from 0001. That one
-- is keyed by parent_id with three fixed flags and no channel dimension, so
-- "email me but do not push me" was unrepresentable and students/admins had
-- nowhere to store a choice. It is left in place (nothing new reads it) rather
-- than dropped, because migrations are append-only and the parent portal still
-- writes it until its Settings screen moves over.

-- One row per thing that happened, per recipient. read_at NULL means unread.
-- Rendered into the recipient's language at send time, so title/body are final
-- text, not keys — a later language change does not rewrite old rows.
CREATE TABLE notification (
    notification_id  TEXT PRIMARY KEY,
    user_account_id  TEXT NOT NULL REFERENCES user_account(user_account_id),
    type             TEXT NOT NULL,
    title            TEXT NOT NULL,
    body             TEXT NOT NULL,
    data             TEXT,                       -- JSON: deep-link ids, dedupe keys
    created_at       TEXT NOT NULL DEFAULT (datetime('now')),
    read_at          TEXT
);
-- The inbox query is "my unread, newest first", so this index carries it.
CREATE INDEX idx_notification_user ON notification(user_account_id, created_at);

-- One row per channel attempt, for retries and for answering "why did this not
-- arrive?". inapp is written 'sent' immediately; the rest start 'pending'.
CREATE TABLE notification_delivery (
    notification_delivery_id TEXT PRIMARY KEY,
    notification_id          TEXT NOT NULL REFERENCES notification(notification_id),
    channel                  TEXT NOT NULL CHECK (channel IN ('inapp','email','webpush','mobile')),
    status                   TEXT NOT NULL CHECK (status IN ('pending','sent','failed','skipped_by_preference')),
    sent_at                  TEXT,
    error                    TEXT
);
CREATE INDEX idx_notification_delivery_notif ON notification_delivery(notification_id);

-- Per-user overrides. An absent row means the default for that (type, channel),
-- so only choices that differ from the default are stored. Keyed on
-- user_account_id, which every role has and which already carries
-- language_preference.
CREATE TABLE notification_setting (
    user_account_id TEXT NOT NULL REFERENCES user_account(user_account_id),
    type            TEXT NOT NULL,
    channel         TEXT NOT NULL CHECK (channel IN ('inapp','email','webpush','mobile')),
    enabled         INTEGER NOT NULL DEFAULT 1,
    PRIMARY KEY (user_account_id, type, channel)
);

-- A browser or device push endpoint. Treated as a per-user secret: never
-- returned to another user, never logged. endpoint is unique so re-registering
-- the same browser updates last_seen_at rather than duplicating.
CREATE TABLE push_subscription (
    push_subscription_id TEXT PRIMARY KEY,
    user_account_id      TEXT NOT NULL REFERENCES user_account(user_account_id),
    channel              TEXT NOT NULL CHECK (channel IN ('webpush','mobile')),
    endpoint             TEXT NOT NULL UNIQUE,
    p256dh               TEXT,                   -- webpush key; NULL for mobile tokens
    auth                 TEXT,                   -- webpush key; NULL for mobile tokens
    user_agent           TEXT,
    created_at           TEXT NOT NULL DEFAULT (datetime('now')),
    last_seen_at         TEXT NOT NULL DEFAULT (datetime('now')),
    failed_at            TEXT
);
CREATE INDEX idx_push_subscription_user ON push_subscription(user_account_id);
