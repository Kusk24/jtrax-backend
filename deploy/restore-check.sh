#!/usr/bin/env bash
# Restores the latest Litestream replica to a scratch file and proves it is a
# usable JTrax database. A backup nobody has restored is a hope, not a backup —
# run this after setup and on a schedule. Reads the same environment as
# litestream.yml; exits non-zero on any failure.
set -euo pipefail

config="${LITESTREAM_CONFIG:-/etc/litestream.yml}"
out="$(mktemp -d)/restored.db"
trap 'rm -rf "$(dirname "$out")"' EXIT

litestream restore -config "$config" -o "$out" "$JTRAX_DB_PATH"

integrity="$(sqlite3 "$out" 'PRAGMA integrity_check;')"
if [ "$integrity" != "ok" ]; then
  echo "restore-check: integrity_check failed: $integrity" >&2
  exit 1
fi

# The schema is whole, and the rows that matter most are there.
migrations="$(sqlite3 "$out" 'SELECT COUNT(*) FROM schema_migration;')"
accounts="$(sqlite3 "$out" 'SELECT COUNT(*) FROM user_account;')"
if [ "$accounts" -eq 0 ]; then
  echo "restore-check: restored database has no accounts" >&2
  exit 1
fi
echo "restore-check: ok — $migrations migrations, $accounts accounts"
