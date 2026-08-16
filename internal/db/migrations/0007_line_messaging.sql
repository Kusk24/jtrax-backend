-- 0007_line_messaging.sql — the LINE Official Account inbox.
--
-- The console has had a Messages screen since the first build, running on
-- fixtures, because the ER model has no message table. This is that table.
--
-- Note what is *not* here: nothing links a LINE person to a parent row. The
-- `line_id` the ER model already carries on parent_contact, teacher and admin
-- is a LINE *display handle* (`@someone`) — the Messaging API cannot send to
-- one. It can only send to a `userId`, which is issued per channel and only
-- learned when somebody messages or follows the account. Those columns
-- therefore cannot drive this, and a thread here stands on its own: a LINE
-- person, by their LINE name. Matching them to a family is a later migration
-- if it is ever wanted.

-- The channel credentials, entered by an admin in the console.
--
-- Both secrets are stored sealed, never in clear: the sealing key comes from
-- the environment (LINE_TOKEN_KEY), so a copy of the database — a Turso
-- snapshot, a local .db pulled down to debug — is not a working access token.
-- The application refuses to store credentials at all when that key is absent
-- rather than quietly falling back to plaintext.
--
-- One row, forced by the CHECK. The academy has one Official Account; a
-- per-branch channel would be a different shape and can have its own migration.
CREATE TABLE line_channel (
    channel_row_id     TEXT PRIMARY KEY DEFAULT 'default' CHECK (channel_row_id = 'default'),
    -- Sealed blobs, base64. Never selected into any response.
    access_token_enc   TEXT NOT NULL,
    channel_secret_enc TEXT NOT NULL,
    -- Last few characters of the token in clear, so the console can show which
    -- credential is installed without being able to use it.
    token_hint         TEXT NOT NULL DEFAULT '',
    updated_at         TEXT NOT NULL DEFAULT (datetime('now')),
    updated_by         TEXT REFERENCES user_account(user_account_id)
);

-- One row per LINE person who has ever reached the account. This is the
-- conversation: there is no separate thread table because a channel gives each
-- person exactly one 1:1 chat with the account, and that is what a thread is.
CREATE TABLE line_contact (
    -- The channel-scoped userId from the webhook: 'U' + 32 hex. Not a handle,
    -- not portable between channels, and the only thing the send API accepts.
    line_user_id    TEXT PRIMARY KEY,
    display_name    TEXT NOT NULL DEFAULT '',
    picture_url     TEXT NOT NULL DEFAULT '',
    -- Cleared when the person blocks the account. A blocked contact can still
    -- be read, but sending to one fails, so the composer says so up front.
    followed        INTEGER NOT NULL DEFAULT 1,
    first_seen_at   TEXT NOT NULL DEFAULT (datetime('now')),
    last_message_at TEXT NOT NULL DEFAULT (datetime('now')),
    unread_count    INTEGER NOT NULL DEFAULT 0,
    -- The reply token from this contact's most recent inbound message, and
    -- when it arrived.
    --
    -- This pair is the whole cost model. Answering with a live reply token is
    -- free and unmetered; once it expires the same words go out as a push
    -- message and are billed against the Official Account's monthly quota. The
    -- token is single-use, so it is cleared the moment it is spent.
    reply_token     TEXT,
    reply_token_at  TEXT
);

-- Newest conversations first is the only order the inbox is ever read in.
CREATE INDEX idx_line_contact_recent ON line_contact(last_message_at DESC);

CREATE TABLE line_message (
    line_message_id  TEXT PRIMARY KEY,
    line_user_id     TEXT NOT NULL REFERENCES line_contact(line_user_id),
    direction        TEXT NOT NULL CHECK (direction IN ('In','Out')),
    -- Stickers, images and locations arrive as events with no text. They are
    -- recorded with their kind and an empty body so the thread shows that
    -- something was sent rather than a silent gap in the conversation.
    kind             TEXT NOT NULL DEFAULT 'text'
                     CHECK (kind IN ('text','sticker','image','video','audio','file','location','other')),
    body             TEXT NOT NULL DEFAULT '',
    -- LINE's own id for an inbound message. Unique because LINE may deliver
    -- the same webhook more than once — it guarantees at-least-once, not
    -- exactly-once — and a redelivery must not double-post the message.
    provider_id      TEXT UNIQUE,
    sent_at          TEXT NOT NULL DEFAULT (datetime('now')),
    -- Which member of staff sent it. Null for inbound.
    sent_by          TEXT REFERENCES user_account(user_account_id),
    -- Outbound only. 'reply' was free, 'push' came out of the monthly quota.
    channel_used     TEXT NOT NULL DEFAULT '' CHECK (channel_used IN ('','reply','push')),
    delivery         TEXT NOT NULL DEFAULT 'Sent' CHECK (delivery IN ('Sent','Failed')),
    -- A short reason code, never the provider's error text. Staff need to tell
    -- "we are out of messages this month" from "they blocked us", and neither
    -- of those is served by echoing an upstream payload to a browser.
    failure_reason   TEXT NOT NULL DEFAULT '' CHECK (failure_reason IN ('','quota','blocked','invalid','network'))
);

CREATE INDEX idx_line_message_thread ON line_message(line_user_id, sent_at);
