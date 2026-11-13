.PHONY: build build-js build-embed build-all test vet lint run dev bundle clean
.PHONY: build-cross build-linux build-linux-arm64 build-windows build-darwin
.PHONY: tidy deps i18n-check i18n-missing
.PHONY: db-reset help

BINARY   ?= forgec2-server
GOOS     ?= $(shell go env GOOS)
BIN_EXT  := $(if $(filter windows,$(GOOS)),.exe,)
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS  := -s -w -buildid= -X github.com/forgec2/forgec2/internal/server.ServerVersion=$(VERSION)

# ---------- Core ----------

build:
	go build -ldflags="$(LDFLAGS)" -trimpath -buildvcs=false -o $(BINARY)$(BIN_EXT) ./cmd/server

# go vet includes the agent, which emits expected unsafe.Pointer warnings
# (Windows syscall patterns). Filter those while still FAILING on real findings
# elsewhere. (The old `go vet ./... 2>&1 | grep -v ... || true` pipe could never
# fail — the exit status came from grep and `|| true` forced 0 — so `make vet`
# was a no-op gate. This mirrors the correct check in .github/workflows/ci.yml.)
vet:
	go vet ./internal/... ./pkg/... ./cmd/... 2>vet.err
	@if [ -n "$$(grep -v 'internal/payload/agent' vet.err)" ]; then echo 'go vet findings (excluding known agent warnings):'; grep -v 'internal/payload/agent' vet.err; exit 1; else echo 'go vet clean (agent-only warnings ignored)'; fi

vet-strict:
	go vet ./...

tidy:
	go mod tidy

deps: tidy

test:
	go test ./... -count=1

lint: vet
	@echo "Install golangci-lint for full linting: https://golangci-lint.run/usage/install/"

# ---------- Embedded Frontend ----------

build-js:
	cd frontend && npm run build

# Copy frontend output to embed directory
internal/webdist/dist: build-js
	rm -rf internal/webdist/dist
	cp -r frontend/out internal/webdist/dist

# Full embed pipeline: built frontend --> embed --> Go binary
build-embed: internal/webdist/dist build

# ---------- Cross-compilation (requires build-js first) ----------

build-linux:
	GOOS=linux GOARCH=amd64 go build -ldflags="$(LDFLAGS)" -trimpath -buildvcs=false -o $(BINARY)-linux-amd64 ./cmd/server

build-linux-arm64:
	GOOS=linux GOARCH=arm64 go build -ldflags="$(LDFLAGS)" -trimpath -buildvcs=false -o $(BINARY)-linux-arm64 ./cmd/server

build-windows:
	GOOS=windows GOARCH=amd64 go build -ldflags="$(LDFLAGS)" -trimpath -buildvcs=false -o $(BINARY)-windows-amd64.exe ./cmd/server

build-darwin:
	GOOS=darwin GOARCH=amd64 go build -ldflags="$(LDFLAGS)" -trimpath -buildvcs=false -o $(BINARY)-darwin-amd64 ./cmd/server

build-cross: build-linux build-windows build-darwin

build-all: build-embed

bundle: build-embed

# ---------- i18n ----------

i18n-check:
	go run ./cmd/i18n-tool check

i18n-missing:
	go run ./cmd/i18n-tool missing

# ---------- Run ----------

run:
	./$(BINARY)$(BIN_EXT) -config config.yaml

# POSIX shells (Linux/macOS/Git Bash). Windows cmd.exe users can run
# `set FORGEC2_DEV=1 && go run ./cmd/server -config config.yaml`.
dev:
	FORGEC2_DEV=1 go run ./cmd/server -config config.yaml

# ---------- Database ----------

db-reset:
	@echo "WARNING: This deletes the database. Press Ctrl+C to abort."
	@timeout /t 3 >nul 2>&1 || sleep 3
	rm -f data/db/forgec2.db

# ---------- Cleanup ----------

clean:
	go clean
	rm -rf internal/webdist/dist
	rm -f $(BINARY) $(BINARY).exe
	rm -f $(BINARY)-linux-amd64 $(BINARY)-linux-arm64 $(BINARY)-windows-amd64.exe $(BINARY)-darwin-amd64
	# Stray root artifacts: editor/editor-backup binaries and generated agent
	# payloads that accumulate at the repo root and risk accidental commit.
	rm -f $(BINARY).exe~ agent agent.exe cbeacon.exe cbeacon-release.exe

# ---------- Help ----------

help:
	@echo "ForgeC2 Makefile"
	@echo ""
	@echo "  make build          Build server for the host OS (requires existing internal/webdist/dist)"
	@echo "  make build-js       Build frontend JS"
	@echo "  make build-embed    Build frontend + embed + Go binary (full pipeline)"
	@echo "  make build-all      Alias for build-embed"
	@echo "  make test           Run all Go tests"
	@echo "  make vet            Run go vet"
	@echo "  make tidy           Run go mod tidy"
	@echo "  make build-cross    Build for Linux, Windows, macOS"
	@echo "  make build-linux    Build for Linux amd64"
	@echo "  make build-linux-arm64 Build for Linux arm64"
	@echo "  make build-windows  Build for Windows amd64"
	@echo "  make build-darwin   Build for macOS amd64"
	@echo "  make dev            Run in dev mode"
	@echo "  make clean          Remove build artifacts"
	@echo "  make help           Show this help"
