# jtrax-backend

Go REST API for JTrax (chess-school management): session auth and role-scoped
CRUD over every entity in the ER model, served under `/api/v1`.

Storage is SQLite — a local file in development, a [Turso](https://turso.tech)
database in deployment. Both speak the same dialect, so queries and migrations
are identical either way. Standard library `net/http` only; no web framework.

## Run

```sh
go run ./cmd/server        # http://localhost:8790/health
```

That creates `jtrax.db` in the working directory, applies the migrations, and
seeds the development dataset (Sandy and her children Penny and Uri, Ms.
Serene, the Beginner/Intermediate classes, the Wellington tournament). Every
seeded account signs in with `jtrax-dev-1234`.

Requires Go 1.25+. Configuration is entirely environment-driven — copy
`.env.example` to `.env` to override anything.

## Test

```sh
go test ./...
```

Integration tests build the full schema and drive the API over HTTP, covering
auth, per-role tenancy scoping, and enum validation.

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
