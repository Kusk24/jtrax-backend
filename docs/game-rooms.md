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

Seats reference `user_account`, not `student`, so a teacher can sit down against
a pupil. Reads resolve each seat to a display name and — where the account
belongs to a pupil — a `student_id`, which is what the admin history links to.

## Endpoints

All are under `/api/v1/game-rooms` and require a session.

| Method | Path | Who | Notes |
| --- | --- | --- | --- |
| `POST` | `/game-rooms` | staff | Mints a room and its code. Optional `label`. |
| `GET` | `/game-rooms` | any | Staff see every room (`?status=` filters); a player sees only rooms they are seated in. |
| `GET` | `/game-rooms/{id}` | staff, seated players | Room, move list, the caller's seat, and every legal move. |
| `DELETE` | `/game-rooms/{id}` | staff | Marks the room `Cancelled`. Ends the game; keeps the record. |
| `DELETE` | `/game-rooms/{id}/record` | staff | Removes the room and its moves for good. `409` while the game is `Active`. |
| `POST` | `/game-rooms/join` | Student, Teacher | Body `{"code":"ABC123"}`. Rate-limited to 20/min per IP. |
| `POST` | `/game-rooms/{id}/moves` | seated players | Body `{"move":"e2e4"}` in UCI. |
| `POST` | `/game-rooms/{id}/resign` | seated players | Colour comes from the caller's seat. |
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
