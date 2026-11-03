#!/usr/bin/env bash
set -euo pipefail

# ForgeC2 — Development Environment Setup
# Usage: bash scripts/setup-dev.sh

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(dirname "$SCRIPT_DIR")"
cd "$ROOT_DIR"

echo "=== ForgeC2 Development Setup ==="
echo ""

# Check prerequisites
check_cmd() {
  if ! command -v "$1" &>/dev/null; then
    echo "ERROR: $1 is required but not installed."
    echo "  $2"
    return 1
  fi
}

check_cmd go "https://go.dev/dl/"
check_cmd node "https://nodejs.org/"
check_cmd npm "https://nodejs.org/"

echo "Go:   $(go version)"
echo "Node: $(node --version)"
echo "npm:  $(npm --version)"
echo ""

# Create config.yaml if not present.
# The generated file must pass Config.Validate() on first boot: TLS on (the
# default require_tls_for_auth refuses plain-HTTP cookies), crypto.key
# "ecdh:" (legacy XOR/plaintext modes no longer exist), the logging section
# (not "log:"), and all five REQUIRED 32-byte-hex storage keys (empty values
# are rejected — the legacy JWT-derivation cascade was removed).
gen_hex_key() {
  if command -v openssl &>/dev/null; then
    openssl rand -hex 32
  else
    head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n'
  fi
}

if [ ! -f config.yaml ]; then
  echo "Creating default config.yaml..."
  JWT_SECRET=$(gen_hex_key)
  BEACON_KEY=$(gen_hex_key)
  LOOT_KEY=$(gen_hex_key)
  EXTC2_KEY=$(gen_hex_key)
  BACKUP_KEY=$(gen_hex_key)
  CREDENTIAL_KEY=$(gen_hex_key)
  CSRF_KEY=$(gen_hex_key)
  cat > config.yaml <<EOF
server:
  port: 8000
  host: "0.0.0.0"
  tls_enabled: true
  jwt_secret: "$JWT_SECRET"
  beacon_key: "$BEACON_KEY"
  data_dir: "data"

database:
  path: "data/db/forgec2.db"
  driver: "sqlite"

plugins:
  # Verify the bundled plugin packages by default. Third-party packages must
  # carry a signature from an explicitly configured trusted key.
  trusted_keys:
    - "b5c42a42ed1409ff23e7c410d23bb44e707fad2e9ecd6c026a63c63192c15bf3"
  require_signed: true
  max_concurrent: 4

implant:
  default_interval: 10
  default_jitter: 25

crypto:
  key: "ecdh:"
  loot_key: "$LOOT_KEY"
  extc2_key: "$EXTC2_KEY"
  backup_key: "$BACKUP_KEY"
  totp_key: "$CREDENTIAL_KEY"
  csrf_key: "$CSRF_KEY"

logging:
  level: "info"
EOF
  chmod 600 config.yaml
  echo "  config.yaml created (edit to taste; secrets are 0600)"
else
  echo "config.yaml already exists, skipping."
fi
echo ""

# Create data directories
echo "Creating data directories..."
mkdir -p data/db
mkdir -p data/loot
mkdir -p data/screenshots
mkdir -p data/payloads
mkdir -p data/logs
echo "  data/ directories ready"
echo ""

# Install Go dependencies
echo "Installing Go dependencies..."
go mod download 2>/dev/null || go mod tidy
echo "  Go dependencies ready"
echo ""

# Install frontend dependencies and build
echo "Installing frontend dependencies..."
cd frontend
npm ci 2>/dev/null || npm install
echo ""
echo "Building frontend..."
npm run build
cd "$ROOT_DIR"
echo "  Frontend built"
echo ""

# Copy frontend to embedded dist
echo "Copying frontend to embedded dist..."
rm -rf internal/webdist/dist
cp -r frontend/out internal/webdist/dist
echo "  Embedded dist ready"
echo ""

# Build the server binary
echo "Building forgec2-server..."
go build -ldflags="-s -w" -o forgec2-server.exe ./cmd/server
echo "  Binary ready: forgec2-server.exe"
echo ""

# Run backend tests (CI package set)
echo "Running backend tests..."
go test ./internal/config/... ./internal/crypto/... ./internal/db/... ./internal/malleable/... ./internal/obfuscation/... ./internal/plugin/... ./internal/server/... ./pkg/... -count=1 -timeout 5m 2>&1 || true
echo ""

echo "=== Setup Complete ==="
echo ""
echo "Start the server:"
echo "  ./forgec2-server.exe -config config.yaml"
echo ""
echo "Or build, deploy and verify in one step (build + restart + health + rollback):"
echo "  bash scripts/deploy.sh          # Linux/macOS"
echo "  powershell -File scripts/deploy.ps1   # Windows"
echo ""
echo "The console binds 0.0.0.0 (reachable from other hosts). Opening this"
echo "server's own address works without configuration; server.allowed_origins"
echo "is only needed when a console hosted on a DIFFERENT origin calls this API."
