#!/bin/bash
# DEPRECATED - superseded by deploy.sh + docker-compose.yml in this directory.
#
# This script provisions the older NATIVE (binary + SQLite) demo described in
# forgec2-demo.service. The deployed demo now runs the published container
# image against PostgreSQL and is provisioned end to end by deploy.sh, which
# also disables the first-boot admin. Kept for reference and rollback only; do
# not run it against the current demo database (it edits SQLite directly).
#
# Provision the isolated read-only demo instance. Run after the first boot:
#
#   sudo bash deploy/demo/provision.sh [DB_PATH] [ROLE_NAME]
#
# Defines (idempotently) the demo-observer custom role and points the public
# `demo` account at it. Set the account password with the shipped tool:
#
#   /opt/forgec2-demo/reset-password -db "$DB" -user demo -pass '<password>'
#
# Read-only by construction: only `*.read` permissions plus ai.use. Every
# write permission is absent, so RequirePermission refuses mutations at the
# handler even if the edge gateway were bypassed. roleGrantsOperator refuses
# ""/viewer/guest, but a NAMED custom role IS an operator (that is how the
# console reaches the WebSocket and the dual-use read routes) — with no write
# permission there is no command surface.
#
# Deliberately NOT granted: users.read / roles.read (account inventory),
# credentials.read (collected secrets), agents.write (the screen-view gate).
# audit.read IS granted: the public demo shows the audit page and its login
# log, so visitor sign-in IPs are visible there by design.
set -eu

DB="${1:-/opt/forgec2-demo/data/db/forgec2.db}"
ROLE="${2:-demo-observer}"
USER_NAME="${3:-demo}"

test -f "$DB" || { echo "db not found: $DB" >&2; exit 1; }

ROLE_JSON='["agents.read","listeners.read","tasks.read","files.read","groups.read","campaigns.read","opsec.read","intel.read","automation.read","notifications.read","ai.use","audit.read"]'

sqlite3 "$DB" <<SQL
INSERT INTO custom_roles (name, description, permissions, created_at, updated_at)
VALUES ('$ROLE', 'Public read-only observer for the demo', '$ROLE_JSON',
        datetime('now'), datetime('now'))
ON CONFLICT(name) DO UPDATE SET
  permissions = excluded.permissions,
  description = excluded.description;

UPDATE users SET role = '$ROLE', is_active = 1 WHERE username = '$USER_NAME';
SQL

echo "role=$ROLE permissions=$ROLE_JSON"
sqlite3 "$DB" "SELECT username, role, is_active FROM users WHERE username = '$USER_NAME';"
