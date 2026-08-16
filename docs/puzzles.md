# Puzzles

Tactics puzzles, set three a day per pupil and graded on the server.

The portal previously shipped **three puzzles hard-coded in the frontend**,
identical for every pupil, every day, forever. `practice_activity` counted
`puzzles_completed` with nothing behind it.

## Why grading is server-side

A solution the client holds is a solution the client can read. These attempts
feed streaks and practice records a parent sees, so they have to mean something —
the same argument as move legality in `game-rooms.md`, and it matters more here
because there is no opponent to notice.

`GET /puzzles/daily` sends the position, rating, themes, side to move and how
many moves are needed. It never sends `moves`. There is a test that fetches the
solution as a teacher and fails if that string appears **anywhere** in what the
pupil is sent — checking for a field called `moves` is not enough, since the
answer leaking under another name is the same leak.

## Where the puzzles come from

Two sources, distinguished by `puzzle.source`:

- **`Lichess`** — the [Lichess puzzle database](https://database.lichess.org/),
  released **CC0 (public domain)**, so unlike the engine there is no licence
  condition on redistributing it. `internal/db/puzzles.csv` is a 60-puzzle
  starter set seeded on every boot.
- **`JCA`** — authored by a teacher through the `puzzles` resource, so the
  position from Tuesday's lesson can be set rather than a random fork.

### The format, and the thing that trips people up

Lichess distributes the position **before** the opponent's move, with that move
first in the solution. The importer applies it, so `puzzle.fen` is always the
position the pupil actually sees, with the pupil to move. Get this wrong and
every puzzle is off by one ply.

### Importing more

The full dump is ~300 MB compressed and ~5 million puzzles — that would swamp a
free-tier database and serve nobody, since a pupil will never reach the end of
ten thousand. The importer filters as it reads, and validates every solution
against the engine, dropping rows that do not play out.

```sh
curl -O https://database.lichess.org/lichess_db_puzzle.csv.zst
zstd -d --stdout lichess_db_puzzle.csv.zst |
  go run ./cmd/importpuzzles -min 400 -max 1600 -limit 5000 -db "$JTRAX_DB"
```

## The daily set

`GET /puzzles/daily` materialises the set as `puzzle_attempt` rows on first
request rather than recomputing it. That is what makes it **stable**: refreshing
the page cannot reroll a puzzle the pupil has just failed.

Puzzles are chosen nearest the pupil's `fide_rating` (falling back to 800) from
those they have never been set. The rating column is the point — a chess school
should not give the same three puzzles to a beginner and a club player.

## Endpoints

| Method | Path | Who | Notes |
| --- | --- | --- | --- |
| `GET` | `/puzzles/daily` | Student | Today's three, without solutions. |
| `POST` | `/puzzles/{id}/attempt` | Student | `{"played": [...], "move": "e2e4"}`. |
| `GET`/`POST`/`PATCH`/`DELETE` | `/puzzles` | staff, Teacher | Authoring. Returns solutions. |

`played` carries only the pupil's **own** moves; the opponent's replies come
from the stored solution, so the server rebuilds the position rather than
trusting the client to report it.

## Authorization

Each rule has a test that fails when the guard is removed; four of five were
confirmed by deliberately breaking them, and the fifth (the leak test) was
rewritten after a mutation slipped past it.

- **Pupils cannot reach `/puzzles`** — it is not in their `ReadRoles`, so the
  bank of solutions is a 403.
- **Attempts are joined against the assignment**, so a pupil can only submit
  against a puzzle they were actually set — otherwise the endpoint is a way to
  grind the whole table for answers.
- **A rejection carries no hint** about what the right move was.
- **Authoring validates the solution** (`Resource.Check`), so a teacher's typo
  is a 400 at the boundary rather than a pupil stuck on an unsolvable board.
- **Only students are set puzzles**; staff and parents get 403.
