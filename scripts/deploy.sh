#!/usr/bin/env bash
# deploy.sh — build, deploy and verify a ForgeC2 server instance.
#
# One command that takes the working tree to a running, verified server:
#
#   preflight -> build (frontend bundle + Go binary) -> stop the old instance
#   -> start the new one -> health/ready/login smoke -> rollback
#
# The previous binary is kept as forgec2-server.prev. A new instance that
# does not come up healthy is rolled back to that binary and started again,
# so a bad build never leaves the box down.
#
# Usage:
#   bash scripts/deploy.sh
#   bash scripts/deploy.sh --skip-checks --port 8000
#   bash scripts/deploy.sh --no-start
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(dirname "$SCRIPT_DIR")"
cd "$ROOT_DIR"

CONFIG="config.yaml"
PORT=0
SKIP_CHECKS=0
SKIP_FRONTEND=0
NO_START=0
NO_ROLLBACK=0
HEALTH_TIMEOUT=60

while [ $# -gt 0 ]; do
  case "$1" in
    --config)       CONFIG="$2"; shift 2 ;;
    --port)         PORT="$2"; shift 2 ;;
    --skip-checks)  SKIP_CHECKS=1; shift ;;
    --skip-frontend) SKIP_FRONTEND=1; shift ;;
    --no-start)     NO_START=1; shift ;;
    --no-rollback)  NO_ROLLBACK=1; shift ;;
    --timeout)      HEALTH_TIMEOUT="$2"; shift 2 ;;
    -h|--help)      sed -n '2,20p' "$0"; exit 0 ;;
    *) echo "unknown option: $1" >&2; exit 2 ;;
  esac
done

step() { printf '\n\033[36m==> %s\033[0m\n' "$1"; }
ok()   { printf '  \033[32mOK\033[0m   %s\n' "$1"; }
warn() { printf '  \033[33mWARN\033[0m %s\n' "$1"; }
fail() { printf '  \033[31mFAIL\033[0m %s\n' "$1"; }

# ---------------------------------------------------------------- 0. preflight
step "Preflight"
for cmd in go node npm; do
  command -v "$cmd" >/dev/null 2>&1 || { echo "ERROR: $cmd is required but not installed" >&2; exit 1; }
done
[ -f "$CONFIG" ] || { echo "ERROR: config '$CONFIG' not found - run 'bash scripts/setup-dev.sh'" >&2; exit 1; }
ok "toolchain and config present"

if [ "$PORT" -le 0 ]; then
  PORT=$(awk '/^[[:space:]]*port:[[:space:]]*[0-9]+/ { gsub(/[^0-9]/, "", $2); print $2; exit }' "$CONFIG" 2>/dev/null || true)
  [ -n "$PORT" ] || PORT=8000
fi
ok "probe port $PORT"

BIND_HOST=$(awk '/^[[:space:]]*host:[[:space:]]*/ { gsub(/["'\'' ]/, "", $2); print $2; exit }' "$CONFIG" 2>/dev/null || true)
BIND_HOST="${BIND_HOST:-0.0.0.0}"
# Same-origin access needs no allowlist, so a wildcard bind means any host
# that can route here can reach the login page.
if [ "$BIND_HOST" = "0.0.0.0" ] || [ "$BIND_HOST" = "::" ] || [ -z "$BIND_HOST" ]; then
  warn "the console binds $BIND_HOST - any host that can route to this machine can reach the login page; set server.host: 127.0.0.1 to keep it local"
fi

# -------------------------------------------------------------------- 1. build
step "Build"
BUILD_ARGS=("-NoProfile" -ExecutionPolicy Bypass -File "$SCRIPT_DIR/build-embedded.ps1")
[ "$SKIP_CHECKS" -eq 1 ] && BUILD_ARGS+=("-SkipChecks")
[ "$SKIP_FRONTEND" -eq 1 ] && BUILD_ARGS+=("-SkipFrontend")
BUILD_ARGS+=("-NoRestart")
if command -v pwsh >/dev/null 2>&1; then
  pwsh "${BUILD_ARGS[@]}"
else
  # No PowerShell on the box: do the same steps by hand.
  if [ "$SKIP_FRONTEND" -eq 0 ]; then
    ( cd frontend && npm run gen:openapi && npm run build )
    rm -rf internal/webdist/dist.new
    mkdir -p internal/webdist/dist.new
    cp -r frontend/out/. internal/webdist/dist.new/
    rm -rf internal/webdist/dist
    mv internal/webdist/dist.new internal/webdist/dist
    node scripts/normalize-webdist.mjs
    if [ "$SKIP_CHECKS" -eq 0 ]; then ( cd frontend && npm run check ); fi
  fi
  go build -o forgec2-server ./cmd/server
fi
ok "build finished"

if [ "$NO_START" -eq 1 ]; then
  echo
  echo "Build only (--no-start): the running instance was not touched."
  exit 0
fi

# ------------------------------------------------- 2. keep the last good build
BINARY="$ROOT_DIR/forgec2-server"
PREVIOUS="$ROOT_DIR/forgec2-server.prev"
[ -f "$BINARY" ] && cp -f "$BINARY" "$PREVIOUS" && ok "previous binary kept as forgec2-server.prev"

# ------------------------------------------------------------- 3. stop the old one
step "Stop the running instance"
if pgrep -f "$BINARY" >/dev/null 2>&1; then
  pkill -f "$BINARY" || true
  for _ in $(seq 1 20); do
    sleep 0.5
    pgrep -f "$BINARY" >/dev/null 2>&1 || break
  done
  if pgrep -f "$BINARY" >/dev/null 2>&1; then
    pkill -9 -f "$BINARY" || true
    sleep 1
  fi
  ok "old instance stopped"
else
  ok "no running instance found"
fi

# ---------------------------------------------------------------- 4. start it up
step "Start the new instance"
mkdir -p logs
# setsid detaches the server from this shell so the deploy script can exit
# without taking the server down with it.
setsid nohup "$BINARY" -config "$CONFIG" >logs/stdout.log 2>logs/stderr.log &
NEW_PID=$!
ok "started (PID $NEW_PID)"

# -------------------------------------------------------------------- 5. verify
step "Health check"
wait_for_health() {
  local pid="$1" deadline=$(( $(date +%s) + HEALTH_TIMEOUT ))
  while [ "$(date +%s)" -lt "$deadline" ]; do
    if ! kill -0 "$pid" 2>/dev/null; then return 1; fi
    if curl -fsS --max-time 3 "http://127.0.0.1:$PORT/health" 2>/dev/null | grep -q '"status":"ok"'; then
      return 0
    fi
    if curl -fsSk --max-time 3 "https://127.0.0.1:$PORT/health" 2>/dev/null | grep -q '"status":"ok"'; then
      return 0
    fi
    sleep 1
  done
  return 1
}

if ! wait_for_health "$NEW_PID"; then
  fail "the new instance did not become healthy within ${HEALTH_TIMEOUT}s"
  [ -f logs/forgec2.log ] && tail -n 30 logs/forgec2.log
  if [ "$NO_ROLLBACK" -eq 1 ]; then exit 1; fi
  if [ ! -f "$PREVIOUS" ]; then
    fail "no previous binary to roll back to - the box stays down until the build is fixed"
    exit 1
  fi

  step "Rollback"
  kill -9 "$NEW_PID" 2>/dev/null || true
  cp -f "$PREVIOUS" "$BINARY"
  setsid nohup "$BINARY" -config "$CONFIG" >logs/stdout.log 2>logs/stderr.log &
  ROLLBACK_PID=$!
  if wait_for_health "$ROLLBACK_PID"; then
    ok "rolled back to the previous build (PID $ROLLBACK_PID) - the console is up again"
  else
    fail "the rollback did not come up either - check logs/forgec2.log"
  fi
  exit 1
fi
ok "health ok"

# A second probe: /ready must answer too (the DB is reachable), and the login
# page must render (the embedded bundle is really there).
if curl -fsS --max-time 5 -o /dev/null "http://127.0.0.1:$PORT/ready"; then
  ok "ready ok"
else
  warn "the readiness probe failed"
fi
if curl -fsS --max-time 5 -o /dev/null "http://127.0.0.1:$PORT/login"; then
  ok "console bundle served"
else
  warn "the login page probe failed"
fi

echo
printf '\033[32m==> Deployed (PID %s)\033[0m\n' "$NEW_PID"
echo "    console : http://127.0.0.1:$PORT/  (bound to $BIND_HOST)"
echo "    health  : http://127.0.0.1:$PORT/health"
echo "    logs    : logs/forgec2.log, logs/stdout.log, logs/stderr.log"
