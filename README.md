# jtrax-backend

Go backend for JTrax (chess-school management). **Scaffold only** — the system
design and architecture are still being worked out, so this repo intentionally
contains nothing beyond a health endpoint. No web framework is chosen yet
(stdlib `net/http` for now) so that decision stays open.

## Run

```sh
go run ./cmd/server        # http://localhost:3000/health
PORT=4000 go run ./cmd/server
```

Requires Go 1.23+.
