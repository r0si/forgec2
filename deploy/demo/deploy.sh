#!/bin/bash
# Deploy the published ForgeC2 image as the public read-only demo.
#
#   sudo bash deploy/demo/deploy.sh
#
# Idempotent: re-running keeps the existing .env/config/secrets and re-applies
# the role/user state. Override with environment variables:
#
#   BASE=/opt/forgec2-demo        install root (logs, nginx.conf, data)
#   PUBLIC_URL=http://host:8000   origin allowed by CORS (config.yaml)
#   PUBLIC_LISTEN=8000            port the checks probe (the real listener is
#                                 `listen` in nginx.conf)
#   DEMO_USER=demo                public observer account
#   DEMO_PASSWORD=...             its password (default is the published demo password)
#   FORGEC2_IMAGE=...             image to run (default in docker-compose.yml)
#
# What it does, in order: secrets -> config -> PostgreSQL role/database ->
# read-only nginx gateway -> container -> provisioning (custom role + demo
# account + verification) -> disable the first-boot admin -> edge assertions.
#
# The old native binary and its unit are not touched here; if a deployment
# migrated from deploy/demo/provision.sh, stop that unit first (the script does
# it when the unit exists) and keep it for rollback.
set -euo pipefail

BASE="${BASE:-/opt/forgec2-demo}"
COMPOSE_DIR="$BASE/compose"
DATA_DIR="$BASE/container-data"
PUBLIC_URL="${PUBLIC_URL:-http://43.143.162.139:8000}"
PUBLIC_LISTEN="${PUBLIC_LISTEN:-8000}"

DEMO_USER="${DEMO_USER:-demo}"
# Remember an explicit CLI/env override: .env may carry an older value.
DEMO_PASS_CLI="${DEMO_PASSWORD:-}"
ROLE_NAME="${ROLE_NAME:-demo-observer}"
# The verification snippet reads these from the environment.
export DEMO_USER ROLE_NAME
# Read-only by construction: every entry is a read permission (or ai.use).
# settings.read and credentials.read are included because visible pages depend
# on them: /api/saved-views and /socks/sessions (Beacons, Pivoting) need
# settings.read, and /lateral fetches /credentials?format=json. Without them
# those pages render but every data call answers 403. No *.write permission is
# present, so RequirePermission still refuses every mutation at the handler.
ROLE_PERMS='["agents.read","listeners.read","tasks.read","files.read","groups.read","campaigns.read","opsec.read","intel.read","automation.read","notifications.read","ai.use","settings.read","credentials.read"]'

API="http://127.0.0.1:18000"
EDGE="http://127.0.0.1:$PUBLIC_LISTEN"

log() { printf '[deploy] %s\n' "$*"; }
die() { printf '[deploy] ERROR: %s\n' "$*" >&2; exit 1; }

# Install a file unless source and destination are the same inode: when this
# script runs from its deployed location (/opt/forgec2-demo/compose), the
# source and target are the same file and `install` refuses.
copy_in_place() { # src dst mode
  if [ "$1" -ef "$2" ]; then
    return 0
  fi
  install -m "$3" "$1" "$2"
}

for cmd in docker openssl curl python3 nginx; do
  command -v "$cmd" >/dev/null 2>&1 || die "missing required command: $cmd"
done

mkdir -p "$COMPOSE_DIR" "$DATA_DIR" "$BASE/logs"
chown 65532:65532 "$DATA_DIR"
chmod 700 "$DATA_DIR"

# ---------------------------------------------------------------- secrets ---
if [ ! -f "$COMPOSE_DIR/.env" ]; then
  log "generating .env (fresh random keys)"
  ADMIN_PASSWORD="${ADMIN_PASSWORD:-Adm1n$(openssl rand -hex 8)!}"
  DEMO_PASSWORD="${DEMO_PASS_CLI:-ForgeC2Demo2026!}"
  cat > "$COMPOSE_DIR/.env" <<EOF
# Image to run. Defaults to the published build; set a mirror or a digest when
# the host cannot reach GHCR directly.
FORGEC2_IMAGE=${FORGEC2_IMAGE:-ghcr.io/r0si/forgec2:edge}
DB_PASSWORD=$(openssl rand -hex 24)
FORGEC2_JWT_SECRET=$(openssl rand -hex 32)
FORGEC2_LOOT_KEY=$(openssl rand -hex 32)
FORGEC2_EXTC2_KEY=$(openssl rand -hex 32)
FORGEC2_BACKUP_KEY=$(openssl rand -hex 32)
FORGEC2_TOTP_KEY=$(openssl rand -hex 32)
FORGEC2_CSRF_KEY=$(openssl rand -hex 32)
ADMIN_PASSWORD=$ADMIN_PASSWORD
DEMO_PASSWORD=$DEMO_PASS
EOF
  chmod 600 "$COMPOSE_DIR/.env"
fi
# shellcheck disable=SC1091
. "$COMPOSE_DIR/.env"
: "${DB_PASSWORD:?}" "${ADMIN_PASSWORD:?}" "${FORGEC2_JWT_SECRET:?}"
DEMO_PASS="${DEMO_PASS_CLI:-${DEMO_PASSWORD:-ForgeC2Demo2026!}}"

# ----------------------------------------------------------------- config ---
# The generated file must stay equivalent to config.yaml.example in this
# directory; that example documents why each section is there, this heredoc is
# what deploy.sh actually writes (with freshly generated keys).
if [ ! -f "$COMPOSE_DIR/config.yaml" ]; then
  log "writing config.yaml"
  cat > "$COMPOSE_DIR/config.yaml" <<EOF
server:
  host: 127.0.0.1
  port: 18000
  tls_enabled: false
  require_tls_for_auth: false
  data_dir: /data
  cert_file: /data/server.crt
  key_file: /data/server.key
  allowed_origins:
    - $PUBLIC_URL
  trusted_proxies:
    - 127.0.0.1
  update_check_enabled: false
  beacon_key: "$(openssl rand -hex 32)"
  tcp_enabled: false
  dns_enabled: false
  grpc_enabled: false
  icmp_enabled: false
  udp_enabled: false
  quic_enabled: false
  ssh_enabled: false
  lportfwd_enabled: false
  auto_recon: []
database:
  driver: postgres
  dsn: ""
auth:
  default_password: "$ADMIN_PASSWORD"
  log_generated_password: false
  session_max_concurrent: 0
crypto:
  key: "ecdh:"
rate_limit:
  extc2:
    api_token: "$(openssl rand -hex 32)"
plugins:
  require_signed: false
backup:
  retain: 0
security:
  require_approval: true
EOF
  # The container runs as uid 65532 and only needs to read this file.
  chown 65532:65532 "$COMPOSE_DIR/config.yaml"
  chmod 400 "$COMPOSE_DIR/config.yaml"
fi

# ------------------------------------------------------------- postgresql ---
# The host cluster is used because this deployment already runs one and the
# demo host cannot reach Docker Hub to pull the postgres image. Replacing it
# with the compose `postgres` profile is a drop-in change: point
# FORGEC2_DB_DSN at that service and drop this section.
systemctl enable --now postgresql >/dev/null 2>&1 || true
if ! runuser -u postgres -- psql -tAc "SELECT 1 FROM pg_roles WHERE rolname='forgec2'" | grep -q 1; then
  log "creating PostgreSQL role forgec2"
  runuser -u postgres -- psql -v ON_ERROR_STOP=1 -c "CREATE ROLE forgec2 LOGIN" >/dev/null
fi
runuser -u postgres -- psql -v ON_ERROR_STOP=1 -c "ALTER ROLE forgec2 WITH PASSWORD '$DB_PASSWORD'" >/dev/null
if ! runuser -u postgres -- psql -tAc "SELECT 1 FROM pg_database WHERE datname='forgec2'" | grep -q 1; then
  log "creating PostgreSQL database forgec2"
  runuser -u postgres -- createdb -O forgec2 forgec2
fi
PGPASSWORD="$DB_PASSWORD" psql -h 127.0.0.1 -U forgec2 -d forgec2 -tAc 'SELECT 1' >/dev/null
log "PostgreSQL ready on 127.0.0.1:5432"

# -------------------------------------------------------------- nginx edge ---
copy_in_place "$(dirname "$0")/nginx.conf" "$BASE/nginx.conf" 0644
log "installing read-only nginx gateway"
nginx -t -c "$BASE/nginx.conf"
if [ -f /run/forgec2-demo-nginx.pid ]; then
  nginx -c "$BASE/nginx.conf" -s reload
else
  nginx -c "$BASE/nginx.conf"
fi

# ------------------------------------------------------------ container app ---
if systemctl list-unit-files forgec2-demo.service >/dev/null 2>&1; then
  log "stopping legacy forgec2-demo.service (kept for rollback)"
  systemctl disable --now forgec2-demo.service >/dev/null 2>&1 || true
fi
# Give a previous listener a moment to release 18000.
for _ in $(seq 1 20); do
  ss -ltn | grep -q '127.0.0.1:18000' || break
  sleep 0.5
done

log "starting forgec2 container"
copy_in_place "$(dirname "$0")/docker-compose.yml" "$COMPOSE_DIR/docker-compose.yml" 0644
(
  cd "$COMPOSE_DIR"
  docker compose up -d
)

healthy=false
for _ in $(seq 1 90); do
  if curl -fsS --max-time 3 "$API/health" >/dev/null 2>&1; then
    healthy=true
    break
  fi
  sleep 2
done
if [ "$healthy" != true ]; then
  log "container failed to become healthy"
  (cd "$COMPOSE_DIR" && docker compose logs --tail=80 forgec2) || true
  die "health check failed; inspect the log above (rollback: docker compose down + re-enable the old unit)"
fi
log "container healthy on $API"

# -------------------------------------------------------------- provisioning ---
login() { # user pass jar
  curl -sS -o /dev/null -w '%{http_code}' -c "$3" -b "$3" \
    --data-urlencode "username=$1" --data-urlencode "password=$2" \
    "$API/api/login"
}
psql_demo() { runuser -u postgres -- psql -d forgec2 -v ON_ERROR_STOP=1 "$@"; }

CJ=$(mktemp)
DJ=$(mktemp)
trap 'rm -f "$CJ" "$DJ"' EXIT

code=$(login admin "$ADMIN_PASSWORD" "$CJ" || true)
if [ "$code" != "302" ] && [ "$code" != "200" ]; then
  log "admin login returned $code; re-enabling admin for provisioning"
  psql_demo -c "UPDATE users SET is_active=true WHERE username='admin'; UPDATE users SET force_logout_at=NULL WHERE username='admin';" >/dev/null
  code=$(login admin "$ADMIN_PASSWORD" "$CJ" || true)
fi
[ "$code" = "302" ] || [ "$code" = "200" ] || die "admin login failed ($code); check ADMIN_PASSWORD in $COMPOSE_DIR/.env"

CSRF=$(awk '$6=="forgec2_csrf" {print $7}' "$CJ" | tail -1)
[ -n "$CSRF" ] || die "no CSRF cookie after login"
AUTH=(-b "$CJ" -H "X-CSRF-Token: $CSRF" -H 'Accept: application/json')

role_body=$(python3 -c '
import json,sys
print(json.dumps({"name":sys.argv[1],"description":"Public read-only observer for the demo","permissions":json.loads(sys.argv[2])}))
' "$ROLE_NAME" "$ROLE_PERMS")

role_id=$(curl -sS -b "$CJ" "$API/api/roles" | python3 -c '
import json,sys
d=json.load(sys.stdin)
rows=d.get("data") or []
print(next((r["id"] for r in rows if r.get("name")==sys.argv[1]),""))
' "$ROLE_NAME" 2>/dev/null || true)
if [ -z "$role_id" ]; then
  log "creating $ROLE_NAME role"
  curl -sS "${AUTH[@]}" -H 'Content-Type: application/json' -d "$role_body" "$API/api/roles" >/dev/null
else
  log "updating $ROLE_NAME role ($role_id)"
  curl -sS "${AUTH[@]}" -H 'Content-Type: application/json' -d "$role_body" "$API/api/roles/$role_id" >/dev/null
fi

user_id=$(curl -sS -b "$CJ" -H 'Accept: application/json' "$API/users" | python3 -c '
import json,sys
d=json.load(sys.stdin)
rows=d.get("users") or []
print(next((u["id"] for u in rows if u.get("username")==sys.argv[1]),""))
' "$DEMO_USER" 2>/dev/null || true)
if [ -z "$user_id" ]; then
  log "creating $DEMO_USER user"
  curl -sS "${AUTH[@]}" \
    --data-urlencode "username=$DEMO_USER" \
    --data-urlencode "password=$DEMO_PASS" \
    --data-urlencode "role=$ROLE_NAME" \
    "$API/users/add" >/dev/null
else
  log "updating $DEMO_USER user ($user_id)"
  curl -sS "${AUTH[@]}" \
    --data-urlencode "role=$ROLE_NAME" \
    "$API/users/$user_id/edit" >/dev/null
  curl -sS "${AUTH[@]}" \
    --data-urlencode "password=$DEMO_PASS" \
    "$API/users/$user_id/password" >/dev/null
fi

# Verify the public account before locking the admin out.
[ "$(login "$DEMO_USER" "$DEMO_PASS" "$DJ" || true)" = "302" ] || die "$DEMO_USER login failed"
curl -sS -b "$DJ" -H 'Accept: application/json' "$API/api/me" | python3 -c '
import json,sys
d=json.load(sys.stdin)
if not d.get("success"):
    raise SystemExit("api/me failed: "+json.dumps(d))
data=d.get("data") or {}
import os
if data.get("role")!=os.environ["ROLE_NAME"]:
    raise SystemExit("role mismatch: "+json.dumps(data))
perms=data.get("permissions") or []
if "agents.read" not in perms or any(p.endswith(".write") or p.endswith(".delete") for p in perms):
    raise SystemExit("permissions unexpected: "+json.dumps(perms))
print("[deploy] %s /api/me role=%s permissions=%d" % (os.environ["DEMO_USER"], data.get("role"), len(perms)))
' || die "demo account verification failed"

# Lock the first-boot admin out of the public deployment. force_logout_at and
# the session revoke make existing cookies useless immediately.
psql_demo <<'SQL' >/dev/null
UPDATE users SET is_active = false WHERE username = 'admin';
UPDATE users SET force_logout_at = now() WHERE username = 'admin';
UPDATE user_sessions SET revoked_at = now()
 WHERE revoked_at IS NULL
   AND user_id = (SELECT id FROM users WHERE username = 'admin');
SQL
log "admin account disabled"

# ------------------------------------------------------------- edge checks ---
check_code() { curl -sS -o /dev/null -w '%{http_code}' --max-time 10 "$@"; }
edge_login=$(check_code -X POST --data-urlencode "username=$DEMO_USER" --data-urlencode "password=$DEMO_PASS" "$EDGE/api/login")
[ "$edge_login" = "302" ] || die "edge POST /api/login -> $edge_login (expected 302)"
edge_ws=$(check_code "$EDGE/ws")
[ "$edge_ws" = "404" ] || die "edge GET /ws -> $edge_ws (expected 404)"
edge_extc2_ws=$(check_code "$EDGE/extc2/ws")
[ "$edge_extc2_ws" = "404" ] || die "edge GET /extc2/ws -> $edge_extc2_ws (expected 404)"
edge_post=$(check_code -X POST "$EDGE/api/agents")
[ "$edge_post" = "403" ] || die "edge POST /api/agents -> $edge_post (expected 403)"
edge_put=$(check_code -X PUT "$EDGE/api/agents")
[ "$edge_put" = "403" ] || die "edge PUT /api/agents -> $edge_put (expected 403)"
edge_payload=$(check_code "$EDGE/payloads/anything")
[ "$edge_payload" = "404" ] || die "edge GET /payloads/... -> $edge_payload (expected 404)"
log "edge checks: login 302, /ws + /extc2/ws 404, POST/PUT /api/agents 403, /payloads 404"

log "done: $PUBLIC_URL ($DEMO_USER / $DEMO_PASS)"
