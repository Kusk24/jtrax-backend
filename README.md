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

The school API is a modular monolith with one server process and one database.
Use vertical slices for school workflows. The handler owns authentication, request
validation and the response contract. A feature operation owns its SQL transaction
and the timing of notifications. The resource registry owns shared CRUD policy.

Start at `internal/api/routes.go` to follow a request. For a read-only example,
`internal/api/history.go` checks who may see the student and calls
`internal/studenthistory.Load` to assemble the timeline. Reuse the existing
domain and integration packages when the workflow already has an owner.

Declare each route once. The router registers that declaration with Gin and the
compatibility mux used for HEAD, escaped or noncanonical paths, and method errors.
Run `go test ./...` after changing a route or workflow.

## Layout

| Path                  | What lives here                                          |
| --------------------- | -------------------------------------------------------- |
| `cmd/server`          | entry point and environment configuration                 |
| `internal/api`        | route table, the generic CRUD engine, resource registry   |
| `internal/auth`       | password hashing and session tokens                       |
| `internal/db`         | connection, embedded migrations, development seed         |
| `internal/httpx`      | JSON helpers, CORS, rate limiting                         |

Endpoints are generated from `internal/api/registry.go`, which declares each
resource's per-role permissions and the SQL scope that limits which rows that
role can see. Authorization is enforced in the query, not by the caller.

## Deploy

Render (Docker) plus Turso, both on free plans — see
[docs/deployment.md](docs/deployment.md) for the full runbook and the
free-tier constraints worth knowing about.

## Docs

Cross-repo knowledge lives in the vault at `../jtrax-docs`; house rules are in
`../CLAUDE.md`.
