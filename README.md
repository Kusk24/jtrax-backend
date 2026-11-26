# JTrax backend

JTrax is the Go API for the chess school's admin, family, and student applications. It runs as one service with one SQLite database. Gin serves the HTTP API under `/api/v1`. Background jobs run in the same process.

## Run locally

Install Go 1.26 or later. From the repository root, start the API on the port the local portals expect.

```sh
PORT=8790 go run ./cmd/server
```

In another terminal, check that the server answers.

```sh
curl -fsS http://localhost:8790/health
# {"status":"ok"}
```

The first run creates `jtrax.db`, applies the migrations, and seeds a fresh local database with demo accounts and school records. For a local admin sign-in, use `admin@jca.ac.th` and `jtrax-dev-1234`.

| Setting | How the server uses it |
| --- | --- |
| `PORT` | Defaults to `8080`. The local web and admin apps expect `8790` unless their `JTRAX_API_URL` points elsewhere. |
| `DATABASE_URL`, `JTRAX_DB` | The first nonempty value selects the database, in that order. The fallback is `jtrax.db` in the working directory. |
| `JTRAX_SEED`, `JTRAX_SEED_PASSWORD` | Seeding demo accounts on a remote libSQL database requires `JTRAX_SEED=1` and an explicit password. Existing accounts cause the demo seed to skip. |
| `ALLOWED_ORIGINS` | Comma-separated browser origins. Defaults to `http://localhost:3000` and `http://localhost:3001`. |

The server reads its process environment. For optional integrations, choose the settings you need from [.env.example](.env.example) and export them in the shell or set them in the process manager before startup.

## How the backend is organized

The monolith is organized by school feature. Start at [route registration](internal/api/routes.go) to find the package that owns an endpoint, or at [job registration](cmd/server/jobs.go) to find the feature that owns a scheduled task.

| Responsibility | Packages under `internal/` |
| --- | --- |
| HTTP routing, request decoding, session authentication, shared CRUD | [api](internal/api), [httpx](internal/httpx), [auth](internal/auth), [records](internal/records) |
| Account access, families, classes, academy settings and forms | [accounts](internal/accounts), [families](internal/families), [classroom](internal/classroom), [academy](internal/academy), [forms](internal/forms) |
| Payments, credits, fees, Stripe and entry payment links | [payments](internal/payments), [fees](internal/fees), [stripepay](internal/stripepay), [entrylinks](internal/entrylinks) |
| Tournament entry, correspondence, published results and standings | [tournaments](internal/tournaments), [entrymail](internal/entrymail), [chessresults](internal/chessresults), [standings](internal/standings) |
| Games, puzzles, Lichess, live updates and student history | [chessplay](internal/chessplay), [game](internal/game), [puzzle](internal/puzzle), [lichess](internal/lichess), [liveevents](internal/liveevents), [studenthistory](internal/studenthistory) |
| School communications, LINE inbox and message delivery | [communications](internal/communications), [lineinbox](internal/lineinbox), [line](internal/line), [notify](internal/notify), [mail](internal/mail), [push](internal/push), [webpush](internal/webpush) |
| Storage and migrations, school time, scheduled work, retention, OCR and encryption | [db](internal/db), [academytime](internal/academytime), [schedule](internal/schedule), [retention](internal/retention), [ocr](internal/ocr), [secretbox](internal/secretbox) |

### Where to make a change

- **Add or change a standard resource.** Find its group in the [resource registry](internal/api/registry.go). The group's definition declares fields, role permissions, row scopes, and hooks. The [shared write code](internal/records/write.go) applies the database change and its transactional hooks together.
- **Change a school workflow.** Keep a simple read in its feature handler. For a multi-step change, use a transaction script in the same package. For example, the [payment void handler](internal/payments/paymentvoid.go) validates the request and writes the response. The [void workflow](internal/payments/void.go) coordinates the payment, credits, enrolment, and audit record in one transaction.
- **Change tournament draft handling.** The [Gin routes](internal/tournaments/tournamentdraft.go) authorize staff, read request inputs, and map outcomes to HTTP. The [draft store](internal/tournaments/draft_store.go) owns preview links, publication, and transactional deletion of a draft and its attachments.
- **Change a public view.** The [tournament results handler](internal/tournaments/publicresults.go) checks publication, selects the results source, and maps it to the response the portals consume. Exercise published and unpublished events when changing this path.
- **Change a background job.** Look up its startup in [jobs.go](cmd/server/jobs.go), then edit the work in its feature package. Jobs that run immediately and then at a fixed interval share the [scheduler](internal/schedule).

## Commands and verification

| Command | Purpose |
| --- | --- |
| [server](cmd/server) | Starts the API and school background jobs. |
| [importpuzzles](cmd/importpuzzles) | Imports Lichess puzzle CSV from standard input. Run `go run ./cmd/importpuzzles -h` for rating, move, limit, and database options. |
| [vapidkeys](cmd/vapidkeys) | Generates a VAPID key pair when provisioning browser push. Keep the private key with the deployment secrets. |

```sh
go test -count=1 ./...
go vet ./...
go build ./cmd/...
```

The [API integration suite](internal/api) drives Gin against temporary databases. Run it when changing routes, status codes, JSON fields, role permissions, or database hooks. Package tests cover feature rules and integrations.

## Database migrations

Append SQL files to [the migration directory](internal/db/migrations) using the next four-digit version and a descriptive name. The server records each full filename in `schema_migration`. Preserve applied filenames and their SQL, including the five historical version pairs. The [version-order test](internal/db/migration_versions_test.go) checks new files for collisions and gaps.

## Deployment and backups

Production runs on AWS Lightsail behind nginx. The API uses a local SQLite file, and Litestream replicates it to S3. Use the [production runbook](https://github.com/Kusk24/jtrax-docs/blob/main/ops/jtrax-on-lightsail.md) for deployment and the [backup and restore runbook](https://github.com/Kusk24/jtrax-docs/blob/main/ops/backups-with-litestream.md) for recovery. The [Litestream configuration and restore check](deploy) live in this repository.

The [Render and Turso guide](docs/deployment.md) documents the previous hosting arrangement.
