# jtrax-backend

Go REST API for JTrax (chess-school management): session auth and role-scoped
CRUD over every entity in the ER model, served under `/api/v1`.

Storage is SQLite — a local file in development, a [Turso](https://turso.tech)
database in deployment. Both speak the same dialect, so queries and migrations
are identical either way. Gin routes requests to the feature handlers.

## Run

```sh
PORT=8790 go run ./cmd/server        # http://localhost:8790/health
```

That creates `jtrax.db` in the working directory, applies the migrations, and
seeds the development dataset (Sandy and her children Penny and Uri, Ms.
Serene, the Beginner/Intermediate classes, the Wellington tournament). Every
seeded account signs in with `jtrax-dev-1234`.

**Set `PORT`.** The server's own default is `8080`, but `jtrax-admin` and
`jtrax-web-app` both call `8790` unless `JTRAX_API_URL` says otherwise. Start it
without `PORT` and the two halves come up healthy, log nothing unusual, and
never reach each other — `curl localhost:8080/health` returns `{"status":"ok"}`
while every console page reports the backend unreachable.

Requires Go 1.26+. Configuration is entirely environment-driven — copy
`.env.example` to `.env` to override anything.

## Test

```sh
go test ./...
```

Integration tests build the full schema and drive the API over HTTP, covering
auth, per-role tenancy scoping, and enum validation.

## Backend design

JTrax deploys one Go server and uses one database. Follow a request from
`internal/api/routes.go` into the package for its school workflow. Scheduled
jobs enter the same packages from `cmd/server/main.go`.

| Where to look | Responsibility |
| --- | --- |
| `internal/api`, `internal/records` | Gin routing, the central resource registry, and shared CRUD handlers |
| `internal/accounts`, `internal/families`, `internal/classroom` | Account access, pupil and family records, attendance and class operations |
| `internal/payments`, `internal/fees` | Payment corrections, credit accounting, checkout and tournament fee calculations |
| `internal/tournaments`, `internal/entrymail`, `internal/entrylinks` | Event registration, results, entry correspondence and payment links |
| `internal/chessplay`, `internal/studenthistory` | Rooms, challenges, puzzle practice, progress and the student timeline |
| `internal/communications`, `internal/lineinbox` | Notifications, delivery preferences and LINE inbox operations |
| `internal/academy`, `internal/forms`, `internal/retention` | Dashboard and contact information, document scanning and retention jobs |

For a worked example, read `internal/payments/paymentvoid.go` and
`internal/payments/void.go`. The handler authenticates the administrator,
validates the reason, and maps the result to the public response. The workflow
owns the transaction that updates the payment, reverses credits, checks the
enrolment and writes the audit record. Its rollback test lives beside it.
Class cancellation in `internal/classroom/classcancel.go` also shows where to
send notifications after a successful commit.

Keep simple reads alongside their handlers. Put multi-step school rules and
their transaction in the owning feature. The registry in
`internal/api/registry.go` lists the resource groups. The corresponding
`registry_*.go` files define their CRUD permissions, row scopes and hooks.
External capabilities such as Stripe checkout, mail delivery, OCR, Lichess
puzzles and tournament results have interfaces at their integration boundaries.

Features register routes with Gin and receive a `*gin.Context`. CORS and
per-route rate limits run as Gin middleware. Use `internal/httpx` for the
school API’s JSON format and strict request decoding. Gin dispatches HEAD
requests and escaped paths to the same handlers as GET and ordinary paths.
The router uses Go’s standard HTTP errors and canonical redirects to preserve
existing client behavior. The integration suite in `internal/api` exercises
the full handler with a temporary database.

## Deploy

Render (Docker) plus Turso, both on free plans — see
[docs/deployment.md](docs/deployment.md) for the full runbook and the
free-tier constraints worth knowing about.

## Docs

Cross-repo knowledge lives in the vault at `../jtrax-docs`; house rules are in
`../CLAUDE.md`.
