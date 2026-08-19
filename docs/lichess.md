# Lichess

Students' Lichess ratings, synced into JTrax.

The academy already sees everything played **here** — `game_room` records every
move — and nothing played **there**. This closes the half of a pupil's practice
the school is otherwise blind to.

## What this is not

- **Not a way to create accounts.** Lichess has no signup API. Every student
  registers themselves on lichess.org; the school cannot provision accounts.
- **Not a general record of a student's chess.** Lichess knows about games
  played on Lichess. Nothing here covers the JTrax board, and nothing covers
  over-the-board games at the academy.
- **Not live.** Lichess pushes nothing — no webhook, no subscription. "Synced"
  means the server reads on a schedule.
- **Not a FIDE rating.** Lichess is Glicko-2 and its numbers run well above
  FIDE. These are stored in `lichess_rating`, deliberately nowhere near
  `student.fide_rating`, which seeds tournaments.

## No credentials at all

There is no API key, no registration and no approval. Everything used here is
public. That is also why the feature costs nothing to run, which matters given
the free-tier constraint.

`LICHESS_API_BASE` exists only to point the client at a stub in tests, or at an
egress proxy. Unset in normal use.

## The verification problem, and the cheap answer

A typed username is a **claim, not a fact**. Nothing stops a pupil entering a
grandmaster's account and appearing at 3000 on a screen their parents see.

So a student who links their own account gets a one-time code —
`JTRAX-K7M2QX94` — and pastes it into their **Lichess bio**. The server reads
the bio back and sets `verified`. A bio is public to read and private to write,
which is exactly the property account verification needs, and it costs one API
call instead of an OAuth round trip and a stored token.

Unverified links are kept rather than refused: a teacher recording a known
username is genuinely useful. They are **marked**, and the console shows the
difference rather than presenting both as fact.

Staff never see the code. They cannot edit a pupil's bio, so handing it to them
would only invite passing it around.

## One request for the whole academy

`POST /api/users` takes up to 300 usernames and returns them all. A class of
thirty is one request, not thirty. Per-student polling is how an integration
gets rate-limited, and Lichess answers 429 when it has had enough — which the
client treats as a reason to stop, not to retry.

The sync is **lazy**: a read finds the data older than six hours and refreshes
it. A timer would be worse here, because the service spends the night asleep on
the free tier. Staff can force a refresh from the console.

## What is stored

| Table | Holds |
| --- | --- |
| `student_lichess` | the link: username, canonical id, verified, verify code |
| `lichess_rating` | current rating per game type |
| `lichess_rating_day` | one rating per type per day, so a term's progress is visible |

A row per game type rather than a column per type: Lichess has a dozen and adds
variants, and a wide table would need a migration the first time the academy
cared about chess960.

**A game type with zero games is not recorded.** Lichess reports 1500
provisional for something never played, and storing that would seed a
leaderboard with a rating nobody earned.

**Provisional ratings are carried through, not dropped.** Lichess flags a rating
until enough games have been played; those numbers swing wildly and must not be
shown as an achievement.

## Endpoints

| Method | Path | Who | Notes |
| --- | --- | --- | --- |
| `GET` | `/lichess/links` | all, scoped | Staff and teachers see everyone; parents their children; students themselves. |
| `GET` | `/lichess/me` | Student | Own link, with the verification code while one is outstanding. |
| `POST` | `/lichess/link` | Student, staff | A student links only their own; staff may link for a pupil. |
| `POST` | `/lichess/verify` | Student | Checks the code in the Lichess bio. |
| `DELETE` | `/lichess/link` | Student, staff | Removes the link and its ratings. |
| `POST` | `/lichess/sync` | staff | Forces a refresh. |
| `GET` | `/lichess/history/{studentId}` | all, scoped | Daily ratings for one game type. |

## Authorization

Nine guards, each with a test, each confirmed by breaking it and watching the
test fail — nine of nine caught.

- **A student links their own account and nobody else's.** Passing another
  pupil's `studentId` is a 403.
- **One Lichess account cannot be claimed by two students** — the second is a
  409, so two pupils cannot both point at the same strong account.
- **Scope is decided in the query**, not by the caller: a student sees one link,
  a parent sees their children, staff and teachers see the academy.
- **History is gated on the same read**, so a student cannot chart a classmate.
- **Only staff force a sync** — it is one outbound request on behalf of the
  whole academy.
- **The verification code is never returned once spent**, and never to staff.
- **Usernames are validated against Lichess's own shape before use**, because
  the only place a caller-supplied string reaches this API is a URL path.

## Things that bite

- **Lichess has a minimum age** — 13 in most jurisdictions, higher under GDPR in
  some. A chess academy teaching young children will have students who cannot
  hold an account at all, so this can never be the primary progress record.
  Check the current terms rather than trusting this line.
- **Usernames are case-preserving but case-insensitive.** The API returns a
  lowercase canonical `id`; that is what every lookup uses, so "Penny" and
  "penny" cannot become two links. The typed form is kept only for display.
- **A closed or renamed account simply drops out of the bulk reply.** Those
  students keep their last known ratings rather than being blanked, so one bad
  username cannot wipe a class.

---

# Playing rated games on Lichess

Everything above is read-only. This part is not: a JTrax game room can *be* a
real, rated game on lichess.org, so a result played on the academy's board moves
a student's actual rating.

## Why the earlier "there is no write API" was wrong

There is one, and it needs no registration:

| Endpoint | Auth | Rated |
| --- | --- | --- |
| `POST /api/challenge/open` | none at all | yes |
| `POST /api/import` | optional token | **never** |
| `POST /api/board/game/{id}/move/{uci}` | `board:play` | yes |

Importing a PGN after the fact produces a real game page and a real analysis
board, but an imported game is never rated. The only way a game played here can
count is for it to *be* a Lichess game while it is played — which is what the
relay does.

## The shape

1. A student grants play access through OAuth2 with PKCE. There is no client
   secret: Lichess does not register applications, so `client_id` is any stable
   string and the flow's security is PKCE alone.
2. The token is sealed with AES-256-GCM (`LICHESS_TOKEN_KEY`) and stored on
   `student_lichess`. It is the most dangerous value in the database — it can
   resign a child's games — and it is never returned by any endpoint.
3. Staff create a room with `lichessRated: true` and a clock.
4. When the second seat fills, white's token issues a challenge to black's
   username and black's token accepts it. Both students must have granted.
5. Every accepted JTrax move is forwarded with the token of the player who made
   it, **after** it is stored and published locally.
6. A stream of the Lichess game runs alongside and has the last word on the
   result.

## Who is authoritative

Split deliberately:

- **JTrax decides legality and turn order.** It already replays the move list on
  every request, and waiting on a network round trip before showing a child
  their own move is what makes a board feel broken.
- **Lichess decides the rated result.** It owns the clock and the rating.

Both run the same rules over the same move list, so they agree about chess. The
one thing they can disagree about is *time* — which is exactly what the stream
is for.

## Detaching

If they disagree anyway, the room stops being rated, records why, and says so on
the board while the pupils are still playing. It never rolls their board back to
match: taking back a move a child already played is worse than losing a rating.

Reasons surfaced to players: `noPlayAccess`, `tokenExpired`, `challengeFailed`,
`opponentDeclined`, `moveRejected`, `unreachable`, `notConfigured`.

## Configuration

| Variable | Purpose |
| --- | --- |
| `LICHESS_TOKEN_KEY` | 32 bytes, base64 or hex. Without it play access is off. |
| `PUBLIC_API_URL` | This server's own public URL — the OAuth `redirect_uri` is built from it and must match byte-for-byte between authorize and exchange. |
| `LICHESS_CLIENT_ID` | Shown on Lichess's consent screen. Defaults to `jtrax.app`. |
| `APP_URL`, `ADMIN_URL` | The only origins a callback may redirect back to. |

## Things that bite

- **Tokens last a year and there is no refresh token.** There is no renewal loop
  to write, only an expiry to watch and a student to ask again. The portal warns
  a month out; `playStatusFor` reports `expiringSoon`.
- **Under-13s cannot hold a Lichess account.** The intended arrangement is an
  account created and held by a parent or teacher with Kid Mode on. `managed_by`
  records who holds it, because that is a safeguarding fact about a child rather
  than a UI detail.
- **Kid Mode is not verified by us.** Reading it needs the `preference:read`
  scope, and widening every child's grant for a check an adult can do once is a
  bad trade. Kid Mode restricts chat, forums and messages — not play.
- **A teacher-versus-pupil game can never be rated.** Only a student row has a
  Lichess link, which is correct: a coaching game should not move a rating.
- **Lichess only accepts certain clocks** — 0, 15, 30, 45, 60, 90, or any
  multiple of 60 up to 10800. Validated at room creation, not at pairing time,
  because by then two pupils are sitting at a board waiting.
- **Repeatedly pairing the same two pupils is what boosting looks like.** Nothing
  here throttles it. Worth watching before a whole class plays rated ladders
  against each other every week.
- **Rooms are resumed after a restart**, but only on the next boot: `resume()`
  re-attaches a stream to every Active rated room. A game that ended while the
  process was down is reconciled on reconnect, because Lichess replays the full
  state before closing a finished game's stream.
