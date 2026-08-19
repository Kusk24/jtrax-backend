# External tournaments from chess-results.com

Staff paste a chess-results.com tournament link; the server pulls the
standings, recognises which rows are the academy's students, and keeps a copy
that everyone signed in can read. The bracket-and-standings public page and the
console's Tournament section both build on the same stored rows.

## The two facts everything follows from

- **Nobody can write to chess-results.com.** It is the publishing side of
  Swiss-Manager, the desktop program arbiters run tournaments with. The upload
  path is that program; there is no API. So this is a one-way read, and "update
  our results to chess-results" is impossible for us and everyone else.
- **There is no read API either.** The site is server-rendered ASP.NET. The
  `internal/chessresults` package is a scraper: header-driven (column names,
  not positions), anchored on the `CRs1` table class Swiss-Manager has emitted
  for years, and pinned by tests against real saved pages in `testdata/`. When
  the markup changes it fails loudly rather than shipping half a table.

## Matching rows to students

FIDE ID first, exact normalised name second. `student.fide_id` (migration 0013)
is the join that survives every spelling — the ranking view usually lacks a
FideID column, so the player-list view is fetched too and the IDs travel across
a name join between the two pages of the same event.

`NormalizeName` folds "Somchai, Niran" and "niran somchai" to one key. It is
exported so the API and the parser cannot drift apart.

## Politeness

The site is donation-run and bans abusive scrapers.

- One refresh costs at most two page fetches.
- A 60-second floor between fetches of one tournament, however triggered.
- Reads serve the stored copy; an unfinished event auto-refreshes only when the
  copy is older than 30 minutes. "Final Ranking" never refreshes again.
- The track endpoint is rate-limited on top, because it turns caller input into
  an outbound request.

## Authorization

- Track, refresh, untrack: staff only.
- Read: any signed-in user — the data is already public on the source site.
- `ParseRef` rejects any host that is not chess-results.com or a mirror
  subdomain: this endpoint makes the *server* issue a GET, and accepting an
  arbitrary host would make the backend an open proxy (SSRF). Covered by tests
  that assert zero outbound fetches on refusal.

## Two bugs worth remembering

- **A map lookup of a missing column returns index 0**, which is the rank
  column — every tournament without a FideID column briefly stored ranks as
  FIDE IDs. Columns are now resolved with a helper that returns -1.
- **The list endpoint deadlocked the whole server**: it queried per row while
  the listing rows still held the local database's single connection
  (`SetMaxOpenConns(1)`). Ids are collected and closed first now, and a
  regression test exists that *hangs* on the bug — a loud failure by design.
