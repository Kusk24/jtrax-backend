# Tournament results

Rounds, pairings and live standings for a JCA event — entered by an arbiter,
readable by anyone once published.

The ER model has tournaments, categories and registrations but nothing for what
happened, which is why the console showed every player on a score of `—` with
zero wins. Those were placeholders. This is the missing half.

## What chess-results.com can and cannot do

Worth recording, because it is the first thing anyone assumes.

**chess-results.com is not a service you can post to.** It is the public output
of **Swiss-Manager**, the pairing program an arbiter runs on a laptop at the
venue: they pair a round locally and click upload. There is no API, no write
endpoint and no partner integration. Publishing a JCA event there means being
the organiser running Swiss-Manager — nothing in JTrax can do it.

**Reading it is feasible.** Checked against the live site:

- Tournament pages are plain GETs — `chess-results.com/tnr1365480.aspx?lan=1`,
  302 to an `S2.` mirror — and the standings are server-rendered HTML.
- `&prt=4&excel=2010` returns the same table as a spreadsheet.
- Every row carries a **FIDE ID**, which is a reliable key for matching a
  student rather than guessing at name spellings.
- `robots.txt` allows everyone *except* `Chess365-Bot`, which is a clear signal
  that scrapers have been blocked by name before.

So importing a student's placing from an external Thai tournament is possible
and is a separate seam. It is scraping either way: fetch only events a JCA
student entered, cache hard, identify honestly.

## The model

Pairings, not a score column per player. A score column is half the size and
cannot answer "who did she play?", cannot print a cross-table, and cannot
compute a Buchholz tiebreak — which is what separates two children who finish
level, and therefore what decides who goes home with the trophy.

| Table | Holds |
| --- | --- |
| `tournament_round` | round number and status |
| `tournament_pairing` | one board: white, black (null = bye), result |

Status is **derived** from the boards — `Playing` while anything is outstanding,
`Completed` when every result is in — so it cannot drift from what it describes.
It is recomputed after pairings are saved as well as after a result is recorded,
because a round of nothing but byes is finished the moment it is paired.

## Scoring, and the distinctions that matter

`internal/standings` is pure and takes no database, because it is the part that
decides who wins.

- A **bye** and a **forfeit** both score a point without a game being played.
  That is not pedantry: an unplayed point contributes nothing to an opponent's
  Buchholz, and a parent asking why their child has a point but no moves
  deserves an answer the data can give.
- **Buchholz** is the sum of the scores of the opponents actually faced. This is
  the plain form, not FIDE's variants that substitute a virtual opponent for
  byes — said explicitly because "Buchholz" alone names half a dozen slightly
  different numbers.
- Players who are level on **every** tiebreak **share a rank**. Joint third is a
  real result; printing 3rd and 4th would invent a difference that does not
  exist.
- A player who has not been paired yet still appears, on nil points. Vanishing
  from your own tournament is not an acceptable way to represent "no games yet".

### Pairing proposals are a convenience, not an engine

`GET /tournaments/{id}/proposed-pairings` sorts by standing and pairs down the
list, skipping rematches while the field allows. A real Swiss uses the **Dutch
system** — colour balance, float history, score groups — and this does none of
it. It exists so an arbiter starts from a sensible list instead of a blank
screen, and every board is editable before it is saved.

## The public page

`GET /api/v1/public/tournaments/{id}/results` needs no session. A results table
only signed-in parents can see is not a results table — a grandparent should be
able to open the link.

Three things make that safe:

1. **Opt-in per event.** `tournament.results_public` defaults to 0. Publishing
   children's names and scores is a decision an organiser makes, not something
   that happens because a table exists.
2. **Rate-limited**, as anything unauthenticated must be.
3. **Strictly less data than the staff view.** Name, category, score, board
   results — what is already pinned to the wall at a tournament hall. No contact
   details, no date of birth, no student id, and no registration ids. The test
   for this searches the payload **by value**, because a change that leaked an
   internal id under a different key would pass a field-name check.

An unpublished tournament returns the same 404 as one that does not exist, so
the endpoint cannot be used to discover which ids are real.

## Endpoints

| Method | Path | Who |
| --- | --- | --- |
| `GET` | `/tournaments/{id}/results` | any signed-in user |
| `POST` | `/tournaments/{id}/rounds` | staff |
| `GET` | `/tournaments/{id}/proposed-pairings` | staff |
| `PUT` | `/tournaments/rounds/{roundId}/pairings` | staff |
| `PATCH` | `/tournaments/pairings/{pairingId}` | staff |
| `GET` | `/public/tournaments/{id}/results` | **anyone**, if published |

Publishing is a normal `PATCH /tournaments/{id}` with `results_public`, through
the registry — a new bespoke endpoint for one boolean would have been noise.

## Authorization

Eight guards, each with a test, each confirmed by breaking it — eight of eight
caught.

- **Only staff pair rounds and record results.** Teachers, parents and students
  get 403; a teacher recording their own pupil's game is a different feature.
- **Setting pairings replaces the round**, so re-pairing cannot leave a player
  on two boards through an orphaned row.
- **Every player must be registered for that tournament**, nobody may appear
  twice in a round, and nobody plays themselves — the last also enforced by a
  CHECK so no future caller can route around it.
- **Results are validated against a fixed list**, so a typo is a 400 rather than
  a constraint violation surfacing as a 500.

## Things that bite

- The round-status query originally referenced the pairing table from inside an
  `UPDATE` on the round table, which silently matched nothing — and the handler
  swallowed the error and returned 200, hiding it. It is now a small helper that
  logs. A "non-fatal" error branch that returns success is how a broken query
  survives a test suite.
- `results_public` is an `ALTER TABLE ... ADD COLUMN`, which SQLite allows and
  which keeps the migration append-only.
