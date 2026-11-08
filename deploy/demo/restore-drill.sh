#!/bin/bash
# Restore drill for the demo backup. A backup is only real if it has been
# restored somewhere and the row counts match the live database.
#
#   sudo bash deploy/demo/restore-drill.sh [dump.sql.gz]
#
# With no argument, takes the newest dump from OUT (default
# /var/backups/forgec2-demo, same default as backup.sh). Restores into a new
# database forgec2_drill_restore on the same cluster via the postgres
# superuser, compares counts on the tables that matter, then drops it.
#
# Requires: sudo access to the postgres superuser (local peer auth) and the
# same .env as the server (DB_PASSWORD).
set -euo pipefail

OUT=/var/backups/forgec2-demo
DUMP="${1:-}"
if [ -z "$DUMP" ]; then
  DUMP=$(ls -t "$OUT"/forgec2-*.sql.gz 2>/dev/null | head -1)
fi
[ -n "$DUMP" ] && [ -r "$DUMP" ] || { echo "no dump to restore (pass a path or keep one in $OUT)" >&2; exit 1; }

ENV_FILE="${BACKUP_ENV:-/opt/forgec2-demo/compose/.env}"
# shellcheck disable=SC1090
. "$ENV_FILE"
export PGPASSWORD="${DB_PASSWORD:?DB_PASSWORD missing?}"

echo "dump: $DUMP ($(stat -c%s "$DUMP") bytes)"
sudo -u postgres psql -c "DROP DATABASE IF EXISTS forgec2_drill_restore" -q
sudo -u postgres createdb -O forgec2 -E UTF8 forgec2_drill_restore
gzip -dc "$DUMP" | psql -h 127.0.0.1 -U forgec2 -d forgec2_drill_restore -q -v ON_ERROR_STOP=1 > /dev/null
echo "restore: ok"

for table in users implants tasks tenants agent_groups agent_tags; do
  live=$(psql -h 127.0.0.1 -U forgec2 -d forgec2 -tAc "SELECT COUNT(*) FROM $table")
  restored=$(psql -h 127.0.0.1 -U forgec2 -d forgec2_drill_restore -tAc "SELECT COUNT(*) FROM $table")
  echo "table $table: live=$live restored=$restored"
  [ "$live" = "$restored" ] || { echo "MISMATCH on $table" >&2; exit 1; }
done
echo "restore drill: PASS (counts match)"
sudo -u postgres dropdb forgec2_drill_restore
