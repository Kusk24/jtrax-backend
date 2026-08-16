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
