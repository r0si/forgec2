#!/bin/bash
# Dump the demo PostgreSQL database. The server's built-in backup is
# SQLite-only and refuses to run under PostgreSQL, so the demo backs up with
# pg_dump from the host cluster.
#
#   sudo bash deploy/demo/backup.sh [--keep N] [--out DIR]
#
# Defaults: keep 7 days of dumps in /var/backups/forgec2-demo. Runs fine
# from cron or a systemd timer, e.g.:
#   0 2 * * * root /opt/forgec2-demo/compose/backup.sh
# Requires the same .env the server uses (DB_PASSWORD); pass BACKUP_ENV to
# override.
set -euo pipefail

KEEP=7
OUT=/var/backups/forgec2-demo
ENV_FILE="${BACKUP_ENV:-/opt/forgec2-demo/compose/.env}"

while [ $# -gt 0 ]; do
  case "$1" in
    --keep) KEEP="$2"; shift 2 ;;
    --out) OUT="$2"; shift 2 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done

[ -r "$ENV_FILE" ] || { echo "env file not readable: $ENV_FILE" >&2; exit 1; }
# shellcheck disable=SC1090
. "$ENV_FILE"
: "${DB_PASSWORD:?DB_PASSWORD missing in $ENV_FILE}"

mkdir -p "$OUT"
stamp=$(date +%F-%H%M%S)
file="$OUT/forgec2-$stamp.sql.gz"

PGPASSWORD="$DB_PASSWORD" pg_dump -h 127.0.0.1 -U forgec2 -d forgec2 \
  --compress=6 --no-owner --no-privileges | gzip > "$file.tmp"
mv "$file.tmp" "$file"
echo "backup written: $file ($(stat -c%s "$file") bytes)"

# Prune everything older than KEEP days.
find "$OUT" -name 'forgec2-*.sql.gz' -mtime +"$KEEP" -print -delete
