# Deploying jtrax-backend

Free hosting: the API runs as a Docker service on **Render**, data lives in
**Turso** (SQLite over HTTP). The portals stay on Vercel and reach the API
through their own `/api` proxy route, so the browser never calls this service
directly and no CORS configuration is needed.

Cross-repo context lives in the vault: `../jtrax-docs/decisions/` and
`../jtrax-docs/ops/`.

## Why this pairing

Turso speaks SQLite, so every query and both migration files run unchanged —
no Postgres port, no placeholder rewriting. It is reached over HTTP, which
suits a host that sleeps: there is no connection pool to re-establish on wake.

## One-time setup

### 1. Create the database

```sh
brew install tursodatabase/tap/turso
turso auth login
turso db create jtrax
turso db show jtrax --url          # -> libsql://jtrax-<org>.turso.io
turso db tokens create jtrax       # -> the auth token
```

### 2. Create the Render service

Render dashboard > **Blueprints** > **New Blueprint Instance**, point it at this
repo. `render.yaml` describes the service; Render will prompt for the values
marked `sync: false`:

| Variable           | Value                                                    |
| ------------------ | -------------------------------------------------------- |
| `DATABASE_URL`     | the `libsql://…` URL from above                           |
| `TURSO_AUTH_TOKEN` | the token from above                                      |
| `ALLOWED_ORIGINS`  | your Vercel URLs, comma-separated                         |

The URL and the token are deliberately separate variables so the secret is
never part of a value that gets logged or pasted into a dashboard field; the
server folds them together at startup and redacts the result before logging.

If Render rejects `region: singapore` on the free plan, change it to `oregon`.

### 3. Seed the demo data (once)

Migrations run automatically on every boot. Seeding does **not** — a public URL
seeded with the password published in this repository would hand anyone an
admin account.

To seed, temporarily add both variables and redeploy:

```
JTRAX_SEED=1
JTRAX_SEED_PASSWORD=<a real password you choose>
```

Then **remove both** and redeploy again. Seeding is a no-op once any account
exists, so leaving them set is not destructive — but it keeps a password in the
service configuration for no reason.

### 4. Create the real staff sign-ins

The seed is a demo fixture and refuses to touch a database that already has
accounts, so it cannot be used to add a colleague or fix a forgotten password.
`JTRAX_STAFF` can. Set it to a JSON array and redeploy:

```
JTRAX_STAFF=[{"email":"head@jca.ac.th","password":"<choose one>","role":"Admin","name":"JCA Head Office","phone":"02-123-4567"},{"email":"front@jca.ac.th","password":"<choose another>","role":"Receptionist","name":"Front Desk"}]
```

`role` is `Admin` or `Receptionist` — the two roles the admin console signs in.
An Admin sees every section; a Receptionist does not get Admins, Academy or
Settings. Passwords must be at least 8 characters.

It runs on every boot and is idempotent: an account that does not exist is
created, one that does has its password, role and name updated. That makes it
the password-reset path too — and because changing a password ends the sessions
it opened, whoever held the old one is signed out.

**Remove the variable once the accounts exist.** Unlike `JTRAX_SEED` this one
keeps applying, so leaving it set means live passwords sit in the Render
dashboard and every deploy silently resets them.

Further accounts are easier from the console itself: the Admins page creates a
real `user_account` and shows a generated temporary password once.

### 5. Point the portals at it

In each Vercel project (`jtrax-web-app`, `jtrax-admin`) set:

```
JTRAX_API_URL=https://jtrax-backend.onrender.com
```

Redeploy. Nothing else changes: `app/api/[...path]/route.ts` already reads it.

### 6. Turn on the keep-alive

In this repo: Settings > Secrets and variables > Actions > **Variables** >
`BACKEND_URL` = your Render URL. `.github/workflows/keep-alive.yml` then pings
`/health` every 10 minutes.

## The free-tier constraints that actually bite

**Spin-down.** A free service sleeps after 15 minutes idle and takes 30-60s to
wake. Vercel gives up before that, so a sleeping backend shows as a 504 in the
portals, not as a slow page. The keep-alive workflow is what prevents this, not
a nicety.

**Instance hours.** The free plan allows 750 per workspace per calendar month.
Keeping one service awake all month costs ~730, which fits with ~20 hours to
spare. That margin only exists while `jtrax-backend` is the *only* free service
in the workspace — adding a second one will exhaust the pool and suspend both
until the month rolls over.

**Turso free plan.** 100 databases, 5 GB, 500M row reads per month. The whole
JTrax dataset is a few MB; reads are the limit that would move first, and the
portals fetch every collection on load.

## Operating it

```sh
# tail logs
render logs -r jtrax-backend --tail      # or the dashboard

# inspect the live database
turso db shell jtrax
turso db shell jtrax "SELECT role, COUNT(*) FROM user_account GROUP BY role"

# back it up
turso db dump jtrax > jtrax-$(date +%F).sql
```

Turso keeps 24h of point-in-time restore on the free plan; `turso db dump`
before anything schema-shaped is still worth it.

## Local development is unchanged

```sh
go run ./cmd/server     # jtrax.db, auto-seeded, dev password
```

`DATABASE_URL` unset means a local file, which seeds itself with
`db.DevPassword`. To rehearse against Turso, put the real values in `.env`
(gitignored) and export them.

## Adding a migration

Append `internal/db/migrations/NNNN_name.sql`. It applies on next boot and is
recorded in `schema_migration`. Migrations are append-only — never edit one
that has run anywhere.

Statements are split and applied one at a time (`internal/db/statements.go`)
because the libSQL protocol takes one statement per request. The splitter
handles quoted strings and `--` comments but **not** an inner `;`, so a trigger
body would need the splitter taught about `BEGIN … END` first.

## Forgot-password email

`POST /api/v1/auth/forgot-password` mails a single-use link that expires in an
hour; `POST /api/v1/auth/reset-password` spends it. Both are rate limited —
forgot-password to 3/minute per IP, because each accepted call sends mail to
someone else's inbox and an unthrottled one lends the academy's sending
reputation to a spammer.

Delivery is plain SMTP, so the provider is a configuration choice rather than a
code dependency:

| Variable | Notes |
|---|---|
| `SMTP_HOST` | e.g. `smtp-relay.brevo.com` |
| `SMTP_PORT` | `587` unless the provider says otherwise |
| `SMTP_USERNAME` / `SMTP_PASSWORD` | from the provider; never committed |
| `MAIL_FROM` | must be an address the provider let you verify |
| `APP_URL` | public web-app URL — the link is `<APP_URL>/reset-password?token=…` |
| `ADMIN_URL` | public admin-console URL, used for staff accounts |

**Leaving `SMTP_HOST` or `MAIL_FROM` unset is not an error.** The endpoint still
answers normally and writes the reset link to the log instead, marked
`SENSITIVE`. That keeps a fresh clone usable without a mail server, and it means
a deploy that forgot to configure mail fails quietly rather than 500-ing — so
check the logs once after enabling it.

Picking a provider on the free-and-no-card constraint: Brevo's free tier allows
a few hundred messages a day to arbitrary recipients with a verified sender
address, which is the shape this needs. Resend's free tier is generous but
sending to addresses other than your own requires a verified domain. Gmail with
an app password works and is capped around 500/day, at the cost of tying
delivery to a personal account.

Two behaviours worth knowing when reading the code:

- The link points at the console for `Admin`/`Receptionist` and at the web app
  for everyone else. The role is read from the account on the server, never
  taken from the request — otherwise a caller could aim the link at their own
  host. With `ADMIN_URL` unset, staff fall back to `APP_URL`.
- The response to forgot-password is identical for a registered and an
  unregistered address. Anything else would let someone discover which families
  attend the academy by trying addresses.
- Completing a reset **deletes every session for that account** and voids any
  other outstanding link. If the reset happened because somebody else knew the
  old password, leaving their session alive would defeat the point.
