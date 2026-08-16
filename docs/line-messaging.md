# LINE messaging

A LINE Official Account inbox inside the admin console: parents write to the
academy's LINE account, and the front desk answers from the browser without
opening the LINE app.

The console has had a Messages screen since the first build. It ran on fixtures
and said so, because the ER model has no message table. This is that table.

## Before you build on this: LINE already gives you a free inbox

LINE hosts a browser chat console at **chat.line.biz**, included with every
Official Account. Staff sign in with a LINE Business ID and answer messages
there. If "reply without opening the LINE app" is the whole requirement, that
tool already does it and costs nothing to run.

What it does not do is put the conversation next to the student's record, the
credit balance and the attendance history — which is the reason to have it here.
That is the trade this feature is making, and it is worth restating whenever
someone asks why the academy maintains its own inbox.

There is no official embeddable widget for the staff inbox. LINE's website
plugin is an *add-friend* button for visitors, pointing the other way, and
`chat.line.biz` sets `X-Frame-Options`, so it cannot be dropped into an iframe.
Talking to the Messaging API directly is the only way to have this in the
console.

## What is deliberately not here

**Nothing links a LINE person to a parent row.** A thread stands on its own: a
LINE person, by their LINE name and picture.

Note that the `line_id` the ER model already carries — on `parent_contact`,
`teacher` and `admin` — **cannot** drive this. That column holds a LINE display
handle (`@someone`). The Messaging API only accepts a `userId`: a 33-character
`U…` string, scoped to one channel, which you only learn when that person
messages or follows the account. The two are not interchangeable and there is no
lookup from one to the other.

Matching threads to families is a later migration if it is ever wanted.

## The cost model, which drives the design

Two ways to send, and the difference is the whole economics of the feature:

| | Reply | Push |
| --- | --- | --- |
| Needs | a `replyToken` from an inbound event | just the `userId` |
| When | shortly after they wrote | any time |
| Cost | **free, unmetered** | **counts against the monthly allowance** |

A school answering a parent an hour later is paying for something that would
have been free at the time. So `deliver` in `internal/api/line.go` tries the
reply token first and falls back to push when LINE rejects it — a rejected token
costs one wasted call, while not trying costs a billed message every time. The
token is single-use and cleared as soon as it is spent.

Every outbound row records which transport it used (`channel_used`), so the
console can show where the allowance went.

**The monthly allowance is the real limit on this feature, not hosting.** Check
the current number for your plan on LINE's own pricing page for Thailand — it is
region-specific and it changes. `GET /line/channel` reports the live figure from
LINE, which is the number to trust.

## Credentials

An admin pastes the channel access token and channel secret into Settings.
That is a deliberate exception to "secrets come from the environment": these
belong to the academy's LINE account, not to the deployment, and an admin
rotating them should not need a redeploy.

They are sealed with AES-256-GCM before storage (`internal/secretbox`). The
sealing key comes from **`LINE_TOKEN_KEY`** in the environment, so the house rule
still holds — the environment holds the key, the database holds ciphertext. A
copy of the database is therefore not a working access token, which matters
because copies are ordinary: a Turso snapshot, a `.db` pulled down to debug.

```sh
openssl rand -base64 32   # → LINE_TOKEN_KEY
```

Without that key the console **refuses to store credentials** rather than
falling back to clear. There is no degraded mode, because the degraded mode is a
leak.

Rotating the key does not lose messages. It makes the stored credentials
unreadable; an admin re-enters them and carries on. GCM's authentication tag
turns that into a clear error rather than a baffling 401 from LINE.

## Setting it up

1. Create an Official Account, then a **Messaging API** channel for it in the
   LINE Developers console. Both are free and neither needs a card.
2. Set `LINE_TOKEN_KEY` on the service and redeploy.
3. In JTrax: **Settings → LINE**, paste the channel access token and channel
   secret. The token is verified against LINE before it is stored, so a bad
   paste fails immediately rather than at the first message.
4. Copy the webhook URL shown on that screen into the LINE console and enable
   the webhook. It is `https://<api-host>/api/v1/line/webhook`, derived from the
   request rather than configured, so it is right by construction.
5. In LINE Official Account Manager, turn off **auto-reply** and the **greeting
   message**, or the account answers alongside the console.

**Check the Chat setting in Official Account Manager.** LINE's own chat feature
and the Messaging API webhook interact, and the behaviour has changed between
versions of that console — verify which events still reach the webhook with Chat
switched on before relying on both at once.

## Endpoints

| Method | Path | Who | Notes |
| --- | --- | --- | --- |
| `POST` | `/line/webhook` | LINE | Signature-gated, rate-limited, 1 MB cap. |
| `GET` | `/line/conversations` | staff | Threads, newest first, with previews. |
| `GET` | `/line/conversations/{id}` | staff | Last 200 messages. |
| `POST` | `/line/conversations/{id}/messages` | staff | `{"text": "…"}`. |
| `POST` | `/line/conversations/{id}/read` | staff | Clears the unread badge. |
| `GET` | `/line/events` | staff | SSE; full inbox snapshot on change. |
| `GET`/`PUT`/`DELETE` | `/line/channel` | **Admin** | Credentials and live quota. |

Staff means Admin and Receptionist. **Teachers are not staff here** — the
academy's LINE account is the front desk's, and a teacher answering as the
school is a different feature with a different authorization story.

## Authorization

Every rule below has a test, and each was confirmed by deliberately breaking the
guard and watching the test fail — twelve mutations, twelve caught.

- **The webhook is authenticated by signature, not session.** HMAC-SHA256 over
  the exact bytes received, compared in constant time. The body is read raw and
  parsed only after the check, because re-encoding parsed JSON changes the
  digest. An unsigned or wrongly-signed body is a 401 and writes nothing.
- **The webhook is rate-limited** despite being signature-gated: verification
  happens after the body is read, so an unsigned flood would otherwise be free
  work. The budget is generous, because a real batch from a busy account is
  legitimate traffic.
- **Only staff reach the inbox.** Teachers, parents and students get 403 on
  every route including the event stream.
- **Only an Admin touches the credentials.** A receptionist answers messages but
  does not hold the credential that sends them.
- **The access token never comes back out.** The settings endpoint returns the
  last four characters and nothing else. The test searches the payload **by
  value** rather than for a field called `accessToken` — a change that echoed
  the credential under another name would pass the weaker test while leaking
  exactly the same string. That lesson came from the puzzle-solution leak test,
  which a mutation slipped past for precisely that reason.
- **LINE's own error text never reaches a browser.** Failures are classified to
  one of `quota`, `blocked`, `invalid`, `network`; the provider's words go to
  the log.

## Things that bite

- **LINE delivers webhooks at least once, not exactly once.** The same event can
  arrive twice. `line_message.provider_id` is unique and inserts use
  `INSERT OR IGNORE`, so a redelivery does not double-post or double-count the
  unread badge.
- **The console's "Verify" button posts an empty event batch.** Treating that as
  malformed makes the console report the webhook as broken.
- **Timestamps.** The schema follows the house convention and stores
  `datetime('now')`, which is not ISO 8601 — browsers parse it as *local* time.
  The API converts to RFC 3339 on the way out, or a Bangkok receptionist sees
  every message seven hours out.
- **Text length is counted in runes, not bytes.** Thai is three bytes per
  character, so a byte limit would cut a valid message to a third of its length.
- **Non-text messages.** Stickers and images arrive with no text. They are
  recorded with their kind and an empty body, so the thread shows that something
  was sent rather than a silent gap.
- **Group messages are ignored.** There is no personal `userId` to thread
  against and the academy's account is not in groups.
- **The SSE hub is in-process**, exactly as in `game-rooms.md`. Correct for one
  instance and wrong the moment there are two, and the failure is silent.
