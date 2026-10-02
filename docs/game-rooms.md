# Game rooms

Two people playing chess against each other inside the portals. Staff open a
room in the admin console and read out its code; two signed-in players type the
code and take the seats.

Cross-repo context lives in the vault (`../jtrax-docs/features/`). This file
covers the endpoints and the rules they enforce.

## Why the server owns the rules

Move legality is decided here, by `internal/game`, using `github.com/notnil/chess`.
The alternative — trusting each browser to grade its own game — means a student
who opens dev tools wins every game, and the academy's record of who beat whom
becomes fiction. `notnil/chess` is pure Go with no transitive dependencies, so
`CGO_ENABLED=0` and the `distroless/static` image are unaffected.

Games are replayed from their move list rather than restored from the stored
FEN. Threefold repetition and the fifty-move rule are properties of the
*history*, not the position, and a game rebuilt from a bare FEN can never claim
either. The `fen` column on `game_room` is a cache for quick reads, not the
source of truth.

Threefold repetition and the fifty-move rule are claimed automatically. In
tournament chess a player decides whether to claim; there is no UI for that
decision here and no clock to run out, so a game reaching either would otherwise
continue forever.

## Data

`migrations/0004_game_room.sql`:

- `game_room` — one row per board. `code` is unique; `status` is
  `Open → Active → Finished`, or `Cancelled` if staff pull it. A `CHECK`
  constraint stops one account holding both seats.
- `game_move` — one row per half-move, primary key `(game_room_id, ply)`.

That composite key is the concurrency control. Two clients racing to submit the
same turn cannot both append: the second `INSERT` violates the key and the
handler answers `409` rather than writing a second move into one turn.

`0045_a_game_room_is_monitored.sql` adds what the console needs to watch a class
of boards:

- `timed` — whether a time control was chosen. The 0012 clock columns default
  to 15+10 on every room, so they alone cannot say. A rated room is always
  timed. An unrated one shows its time control as a label only: this board
  keeps no time, so only a rated game (on Lichess's clock) can end on time.
- `clock_white_ms`, `clock_black_ms`, `clock_at` — each side's remaining time as
  the Lichess game stream last reported it, and when. The side to move counts
  down from `clock_at`. Rated games only.
- `draw_offer` — the colour offering a draw while the offer stands.

`0046_an_assigned_game_waits_for_both.sql` adds `white_entered_at` and
`black_entered_at`. A game the office sets up has both seats filled but opens
`Open`; each student presses Enter (`POST /{id}/enter`), and the game becomes
`Active` — and a rated one is paired on Lichess — only when both have. Joining
by code counts as entering. An accepted challenge still opens `Active`.

`0047_a_game_can_be_stopped_and_resumed.sql` adds `stopped_at`. Staff pause a
game in play (`POST /{id}/stop`): it returns to `Open` with its moves
kept, and nobody can move until staff resume it (`POST /{id}/resume`) — the
players cannot resume it, and Enter is refused while it is paused. Resuming
puts it back to waiting: both players press Enter again, and it is in play
once both have, from the same position. A rated game cannot be paused on Lichess, so stopping one aborts the
Lichess side where it still can and the game resumes here unrated
(`lichess_detached_reason = 'stopped'`); the console warns before doing it.

Seats reference `user_account`, not `student`, so a teacher can sit down against
a pupil. Reads resolve each seat to a display name and — where the account
belongs to a pupil — a `student_id`, which is what the admin history links to. A
pupil's seat also carries their non-provisional Lichess `rating` in the game's
speed (limit + 40 × increment, by Lichess's own rule; rapid when untimed).

## Endpoints

All are under `/api/v1/game-rooms` and require a session.

| Method | Path | Who | Notes |
| --- | --- | --- | --- |
| `POST` | `/game-rooms` | staff | Mints a room and its code. Optional `label`; `timed` with `clockLimit`/`clockIncrement` for a time control; `lichessRated`. `whiteStudentId` + `blackStudentId` seat both players — the game shows in both pupils' lists and waits, `Open`, until each has entered. Both or neither; `409` if either is already in an unfinished game. |
| `GET` | `/game-rooms` | any | Staff see every room (`?status=` filters, `?moves=1` adds each game's `sans` and `lastUci`); a player sees only rooms they are seated in. |
| `GET` | `/game-rooms/{id}` | staff, seated players | Room, move list, the caller's seat, and every legal move. |
| `DELETE` | `/game-rooms/{id}` | staff | Marks the room `Cancelled`. Ends the game; keeps the record. |
| `DELETE` | `/game-rooms/{id}/record` | staff | Removes the room and its moves for good. `409` while the game is `Active`. |
| `POST` | `/game-rooms/join` | Student, Teacher | Body `{"code":"ABC123"}`. Rate-limited to 20/min per IP. |
| `POST` | `/game-rooms/{id}/stop` | staff | Pauses a game in play; `409` otherwise. |
| `POST` | `/game-rooms/{id}/resume` | staff | Lets a paused game carry on: it waits for both players to enter again; `409` otherwise. |
| `POST` | `/game-rooms/{id}/enter` | seated players | Marks the caller at the board; the game starts when both have. Idempotent. |
| `POST` | `/game-rooms/{id}/moves` | seated players | Body `{"move":"e2e4"}` in UCI. |
| `POST` | `/game-rooms/{id}/resign` | seated players | Colour comes from the caller's seat. |
| `POST` | `/game-rooms/{id}/draw/offer` | seated players | Stands until the opponent answers or either side moves. |
| `POST` | `/game-rooms/{id}/draw/accept` | the other seated player | Ends the game `1/2-1/2`, reason `Agreement`; forwarded to Lichess on a rated game. |
| `POST` | `/game-rooms/{id}/draw/decline` | the other seated player | Clears the offer. |
| `GET` | `/game-rooms/{id}/events` | staff, seated players | SSE stream of room state. |

## Live updates

`GET /game-rooms/{id}/events` is Server-Sent Events, not a WebSocket. Chess is
turn-based at roughly a move every few seconds, so full duplex buys nothing, and
this is ~100 lines of `net/http` instead of a protocol upgrade. What actually
decided it: `EventSource` reconnects on its own, and the API sleeps after fifteen
idle minutes on the Render free tier — a dropped stream has to heal without the
player noticing.

Each event is a **full snapshot**, not a delta, so a watcher that missed one
still converges and a late joiner needs no replay. A stream opens by sending the
current state, so connecting mid-game is correct without a separate fetch.
Events never carry the room code.

Two things to know before scaling:

- **The hub is in-process.** Correct for one instance, silently wrong for two —
  a move on instance A would never reach a watcher on instance B. Going
  multi-instance means replacing it with a shared bus.
- **Slow subscribers are dropped, not waited for.** A backgrounded tab stops
  reading; the game must not stall for the opponent because of it, so a full
  channel loses events rather than blocking the mover.

Anything proxying this must not buffer. The handler sets `X-Accel-Buffering: no`
for nginx-style proxies; the Next.js `/api/[...path]` route must forward the body
as a stream rather than awaiting it.

## Authorization

Every rule below has a test in `internal/api/games_test.go` that fails when the
guard is removed.

- **Only staff open or cancel a room.** Teachers can play but not mint codes.
- **Parents cannot take a seat.** Every seat taken is a seat a pupil cannot have.
- **The seat claim is a conditional `UPDATE`**, not a read-then-write. Three
  simultaneous joins with the same code produce exactly two seats, one each.
- **Rejoining is not joining.** A player who reloads keeps their seat instead of
  being told the room is full.
- **A room a caller is not in reads as `404`**, not `403`, so room ids cannot be
  probed.
- **Turn order comes from the position**, not from the move count.
- **Room codes are stripped** from any room the caller is neither staff for nor
  seated in. A code is a bearer credential: holding one is how you get a seat.
- **Codes are `crypto/rand`** over a 32-character alphabet with `I`, `O`, `0`
  and `1` removed — those are the ones a child mistypes. Six characters gives
  ~1.07 billion codes, and joining is rate-limited on top.
