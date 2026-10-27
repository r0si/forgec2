# AGENTS.md

Instructions for AI coding agents working in this repository.

## Important: This is a Windows PowerShell 5.1 environment
The `bash` tool runs **PowerShell 5.1**, not bash. Adapt accordingly:
- `head`, `grep`, `rm`, `&&` DO NOT work. Use `Select-Object -First N`, `Select-String`, `Remove-Item`, and chain sequential dependent commands with `if ($?) { ... }`.
- Use `-LiteralPath` for paths containing brackets/spaces.
- Do NOT use `Set-Content`/`Get-Content` on Go source files (corrupts UTF-8). Use the dedicated edit/write tools instead.
- Prefer full cmdlet names (`Get-ChildItem`, `Remove-Item`) over aliases.
- `cd` is discouraged; use the `workdir` parameter of the shell tool instead.

## Project overview
ForgeC2 is a mature Go (net/http + sqlite) C2 server plus a Vite + React TypeScript frontend.
- Server binary: `forgec2-server.exe` (built with `go build ./cmd/server`).
- Default: port **8000**, config `config.yaml`, sqlite DB `data/db/forgec2.db` (users `admin`/`labtest`).
- Version: 2.7.1 (see CHANGELOG.md; single source `VERSION`).

## Frontend build & embedding (IMPORTANT)
The frontend is **Vite** (not Next.js).
- Build: `npm run dev` for dev; production: `npm run build` → outputs to `frontend/out`.
- Frontend is embedded into the Go server via `//go:embed all:dist` in `internal/webdist/webdist.go`, which embeds the directory `internal/webdist/dist`.
- **Canonical sync script**: `scripts/build-embedded.ps1` (uses `Remove-Item` + `Copy-Item -Recurse frontend/out/*` → `internal/webdist/dist`; includes assets; runs `scripts/normalize-webdist.mjs` afterwards). Use this, NOT robocopy directly.
- After syncing, verify with `scripts/check-webdist.mjs` (expects "OK: matches frontend/out").
- **Check out with LF line endings.** CI builds the bundle on Linux and compares it byte for byte, so the committed `internal/webdist/dist` must come from an LF checkout. With `core.autocrlf=true`, `frontend/index.html` is CRLF and vite's static export re-converts the newlines, emitting a doubled CR (`\r\r\n`) that no amount of normalization removes. `check-webdist.mjs` detects exactly that case and prints the fix; the short version is `git config core.autocrlf false` in this clone, then re-checkout and rebuild.
- **The bundle must be byte-identical on every platform.** Two traps, both fixed but easy to re-introduce:
  - The CSS target is pinned to `safari16` (`vite.config.ts`). With an older target, lightningcss downgrades the `oklch()` palette to `lab()` and prints the converted numbers with a platform-dependent precision (36.4219 on Windows, 36.422 on Linux) — one byte changes the CSS hash, then every chunk name and every HTML page name, so the Windows build never matches the CI's Linux build.
  - **No two modules may share a basename.** The bundler disambiguates chunk names from the module path, which Windows and Linux spell differently (`pages/traffic/TrafficPage` vs `pages/beacons/detail/traffic/TrafficPage` produced different chunk names on the two platforms). Keep the page components uniquely named (`BeaconTrafficPage`, `BeaconFilesPage`, …).
  - To refresh the committed bundle from a Linux host: dispatch `.github/workflows/webdist-export.yml` (upload-artifact) and replace `internal/webdist/dist` with the artifact — the same way the Linux visual baselines are refreshed.

## Frontend verification
Node **24.15+** is the floor (CI and release workflows use `24`; Vitest 5 and
jsdom 30 set the minimum). Run from `frontend/`:
- `npm run build` (vite)
- `npm run check` (strict gate running all `check:*` scripts)
- `npm run test` (vitest)
- `npm run lint` (eslint)
- `npx tsc --noEmit` (typecheck)
- `npm run gen:openapi` regenerates `src/lib/api-schema.d.ts`

## Static gates
`npm run check` also runs structural guards over the Go source, and CI runs the
same set through `npm run check:pre`. They were defined as scripts for a while
but never added to either task list (`concurrently -n` still listed the labels),
so they enforced nothing until that wiring was fixed — when you add a gate, add it
to **both** lists. Each one exists because the class of bug it catches was found
by hand at least once; do not weaken one to make a change pass — fix the change.
- `check:route-auth` (`scripts/check-route-auth.mjs`) — every mutating route must
  sit behind `middleware.RequirePermission`/`RequireRole`, or carry a handler-level
  gate. The intentional public routes (login, session extend/logout, the language
  setter, the rate-limited phishing landing) are listed with a reason in the script.
- `check:context-keys` (`scripts/check-context-keys.mjs`) — a gin context key read
  with `c.Get*` must be written by production code; test-only setters do not count,
  and `const` key names are resolved.
- `check:error-sanitization` (`scripts/check-error-sanitization.mjs`) — a 5xx must
  answer a literal or `sanitizeError(...)`, never a raw `err.Error()`.
- `check:tx-queries` (`scripts/check-tx-queries.mjs`) — no query through the pool
  handle inside a transaction block: SQLite is pinned to one connection, so the
  nested query waits forever and the handler hangs instead of erroring.
- `check:command-separators` (`scripts/check-command-separators.mjs`) — a function
  that joins a task command with `"|"` must validate its fields first
  (`validateCommandArg` or `credentialCommandHasSeparator`). The implant splits the
  command on `"|"`, so an unvalidated separator in one field shifts every later
  field and the task silently means something else. That shipped four times:
  password_spray re-targeted the domain, cred_check probed the wrong account **and
  bypassed the lockout fuse**, persistence removed a different entry, uac_bypass
  ran a different payload. In-memory key builders are allowlisted with a reason.
- `TestSuccessEnvelope` / `TestErrorEnvelope` (Go) — ratchets over raw (non-envelope)
  JSON success and error responses, per file, failing in both directions: a new raw
  response, or a file that improved without lowering its number. Update the matching
  `internal/server/envelope*_baseline.txt` in the same commit.

## In-memory registries and eviction
The server keeps several per-agent registries in memory. The recurring bug in this
area is a registry that has a **writer but no reachable eviction** — the entry is
only removed on a clean stop, so a crash, a panicking handler or a closed console
leaves it behind. The visible damage is rarely memory: a stale entry keeps
answering "still active" to code that gates real behaviour.

Rules that came out of those defects:
- **Evict on the read path**, not only in a sweep. `IsScreenMonitoring` expires a
  stale entry when asked; do not assume the periodic cleanup will cover you.
- `cleanupStaleMapEntries` is only reached from `runPeriodicCleanup`, whose ticker
  is `PeriodicCleanupInterval` = **24h**. Anything that must be reclaimed sooner
  needs its own timeout or a lazy check.
- A single-flight reservation (a map entry that blocks a second operation) must
  **expire**: the handler's cleanup is skipped when the handler panics.
- A long-lived map entry that holds payload bytes (a cached frame, a queued tunnel
  frame) must be dropped with the session that owns it.

Current timeouts, and where they live:

| registry | reclaim | value |
|---|---|---|
| `screenMonitorImplants` | lazy in `IsScreenMonitoring` + start-handler sweep | 2 min |
| `screenFrames` | dropped with the expired monitor entry | — |
| `screenMonitorOps` | expired by `reserveScreenMonitorOp` | 2 min |
| `ntlmRelayStore` | sweep in `handleNTLMRelayStatus`, and `add` replaces per agent | 12 h |
| backup snapshots / `.fbk.tmp` | `removeStaleTempFiles` in `cleanupOldBackups` | 1 h |
| `socksRelayEngine` connections | `cleanupStaleSocks` ticker | 5 min |
| `socksRelayEngine` control/rportfwd queues | cleared by `stopSession` | — |

## Audit chain
The audit log is a hash chain (`auditEntryHash`, `PrevHash` -> `EntryHash`).
Three things are easy to get wrong here:

- **Never state that it detects a full rewrite.** The hash is unkeyed, the
  algorithm is public and PrevHash is not part of the entry hash, so it detects
  **partial** edits (one row changed, a row removed, a link rewritten) - the shape
  an operator or a compromised account produces through the app or a DB client. An
  attacker with write access who recomputes every hash is **not** detected; that
  would need a keyed MAC or an external anchor.
- **Anything that deletes rows must leave a marker.** handlePurgeAuditLogs
  writes an `audit_purge` record before deleting, and `verifyAuditChain` accepts a
  discontinuity only at the window start when the id sequence has a gap - a
  legitimate purge must not make /ready cry tampering, and an interior deletion
  must still fail.
- **Keep an out-of-band copy.** GET /api/audit/export streams NDJSON with every
  hashed field plus both hashes, so a third party can verify offline; a later
  rewrite of the whole table cannot match their copy.

/ready reports checks.audit_chain and a warnings entry when verification
fails, but deliberately stays **ready**: a tampered log is an alarm about the past.

## Testing
Backend test suite via CI (`.github/workflows/ci.yml`):
```
go test ./internal/config/... ./internal/crypto/... ./internal/db/... ./internal/malleable/... ./internal/obfuscation/... ./internal/payload/... ./internal/plugin/... ./internal/scripting/... ./internal/server/... ./pkg/... ./cmd/... -count=1 -timeout 8m -coverprofile=coverage.out
```
- The race job runs in two parallel legs: `./internal/server/...` and
  `./internal/db/... ./internal/plugin/... ./internal/scripting/... ./internal/payload/agent/...`
  (`-race -timeout 20m` each). Locally the server leg alone takes 11–14 m, which is why it is not
  bundled with the rest.
- **Windows caveat:** `go test -race ./internal/payload/agent/...` aborts on Windows with
  `fatal error: checkptr: pointer arithmetic result points to invalid allocation`. The agent's
  Windows-only shellcode/PE/EDR code walks raw pointers on purpose, and checkptr (implied by `-race`)
  rejects that arithmetic. CI never sees it because the leg runs on Linux, where those files do not
  compile. To run that package locally use `go test -race -gcflags=all=-d=checkptr=0 ./internal/payload/agent/...`,
  and do not treat the abort as a product bug. Making the package fully checkptr-clean would mean
  rewriting ~40 `unsafe.Pointer(base + offset)` sites to `unsafe.Add` — a separate, risky batch in
  security-sensitive code.
- `go vet ./internal/... ./pkg/... ./cmd/...` — but **ignore `internal/payload/agent`** (known unsafe.Pointer warnings; also filtered in CI).
- CI requires **gofmt** on all changed Go files.

## Build caveats
- `go build ./...` passes (the `data/e2e/` helpers have `//go:build ignore` tags).
- Go version: **1.26.0** (go.mod) with `toolchain go1.26.10` pinned for the standard-library security patches (net/http, crypto/tls, html/template, net/textproto). CI's `setup-go` uses the same 1.26.10.
- sqlite driver is **`github.com/glebarez/sqlite`** (pure Go, no cgo). Any DB-verification temp program MUST use this driver, NOT `gorm.io/driver/sqlite` (which needs cgo).

## CSRF Protection (IMPORTANT)
- Login (`POST /api/login`) now sets `forgec2_csrf` cookie to the session-derived token (`DeriveCSRFToken(session, csrf_key)`), not a random value.
- `CSRFProtect` middleware validates **both** `X-CSRF-Token` header **and** `forgec2_csrf` cookie against the derived token (true double-submit).
- On GET/HEAD, the middleware rotates the cookie to the derived token.
- Frontend `src/lib/api.ts` reads the cookie via `readCsrfCookie()` and echoes it as `X-CSRF-Token` on mutations.
- E2E automation must: login → GET `/api/v1/tasks` (or any authenticated GET) → POST with `X-CSRF-Token` = cookie value.

## C-Implant E2E
- One-click script: `scripts/e2e-c-implant.bat <HOST> <PORT> <USER> <PASS> <AGENT_ID> [TASK_TYPE] [COMMAND]`
- Supported task types: `shell` (default), `wechat_history`
- Release build: `proto/c-implant/build-release.bat` (uses `-Os -s -Wl,--strip-all -ffunction-sections -fdata-sections -Wl,--gc-sections`)
- Local E2E build: `proto/c-implant/build-e2e-local.bat` (gitignored; takes `SECRET_ID`/`SECRET_B64` from env vars or args — no secret is stored in the file)
- Tracked template `build-e2e.bat` contains placeholders only (`YOUR_SECRET_ID`, `YOUR_SECRET_B64`, port 8001).

## Plugin authoring (IMPORTANT)
Plugins (command/hook/report; interpreters python, python3, powershell, pwsh, bash, sh, go, wasm) are packages: a directory with `manifest.yaml` + an entry file.
- Scaffold a package that is valid by construction: `go run ./cmd/plugin-init -name <name> -type <command|hook|report> -interpreter <interpreter> -dir <dir>` (omit `-interpreter` to auto-detect the first one that runs here).
- Validate like the server does, then execute once: `go run ./cmd/plugin-check <dir> -run` (add `-json` for the machine-readable report, `-key <hex>`/`-require-signed` to exercise the trust policy).
- Every finding has a stable code (`manifest.invalid`, `wasm.bad_abi`, `result.bad_json`, …); fix error-level findings only — warnings do not fail. Exit code 1 means errors.
- Contract reference for humans and agents: `docs/PLUGIN_AUTHORING.md` + `docs/schemas/plugin-manifest.schema.json`; tier guarantees/quotas: `docs/PLUGIN_TIERS.md`; WASM ABI v1 sample: `examples/wasm/hello-report`.
- Never hand-edit `digest`/`signature`: stamp with `PLUGIN_SIGNING_KEY=<seed> go run ./cmd/sign-plugin <dir>` (`-gen` to create a key). Native tiers run as real child processes; only `wasm` is sandboxed.

## Reset Password
CLI tool: `go run ./cmd/reset-password -db data/db/forgec2.db -user <user> -pass <newpass>`
Or build: `go build -o reset-password.exe ./cmd/reset-password`

## Mint a Registration Secret (no UI)
CLI tool: `go run ./cmd/mint-reg-secret -config config.yaml` (or `-db <path> -beacon-key <hex>`)
Prints the `set SECRET_ID=` / `set SECRET_B64=` pair for a build. It writes the sealed row straight into `reg_secrets` (no UI, no audit entry) and binds to the first agent id that registers with it. Optional `-tenant <n>` stamps the row with a tenant id so the first agent that registers with it lands in that tenant (0 = legacy/unscoped: the agent goes to the default tenant, the pre-batch-N behavior). Used by `scripts/e2e-c-implant-local.ps1`.

## Deploying the server
One command (build + webdist sync + gates + restart + health + rollback):
- Windows: `powershell -ExecutionPolicy Bypass -File scripts/deploy.ps1`
- Linux/macOS: `bash scripts/deploy.sh`

It preflights the toolchain and the config, builds through `scripts/build-embedded.ps1` (the canonical
frontend → `internal/webdist/dist` sync), keeps the previous binary as `forgec2-server.exe.prev`
(`forgec2-server.prev` on Linux), stops the old instance and waits for the port, starts the new one
detached with `logs/stdout.log` + `logs/stderr.log`, probes `/health` (HTTP and the self-signed HTTPS
profile), `/ready` and `/login`, and rolls back to `.prev` — then starts it — when the new build does
not come up healthy. The log tail is printed on failure. Flags: `-SkipChecks`, `-SkipFrontend`
(backend-only change), `-NoStart` (build only), `-NoRollback`, `-Port`, `-HealthTimeoutSeconds`.

Manual equivalent when a step has to be isolated:
1. `npm run build` (from `frontend/`)
2. Sync dist via `scripts/build-embedded.ps1` (add `-NoRestart` to build without touching a running server)
3. `go build -o forgec2-server.exe ./cmd/server`
4. Kill running instance: `taskkill /f /im forgec2-server.exe`
5. Start detached and redirect the child's stdout/stderr: without the redirect the server inherits the
   caller's console handles and a script that started it blocks until the server exits.
   `Start-Process -WindowStyle Hidden -FilePath ".\forgec2-server.exe" -ArgumentList "-config config.yaml" -PassThru -RedirectStandardOutput logs\stdout.log -RedirectStandardError logs\stderr.log`
6. Logs go to `logs/forgec2.log`. Verify with `GET /health` (expects `ok`).
7. The server binds `0.0.0.0` by default (reachable from other hosts). A browser on another host also
   needs its hostname in `server.allowed_origins`, otherwise the API refuses it as cross-origin — the
   server logs a startup warning for a wildcard bind with an empty list.

## Releasing
Push a `v*` tag. `.github/workflows/release.yml` cross-compiles linux/windows × amd64/arm64, then
publishes both the raw binaries and the one-click archives `forgec2-v<version>-<os>-<arch>.zip|tar.gz`
(embedded-console binary, `start.cmd`/`start.sh`, `config.example.yaml`, `QUICKSTART.txt`), each with a
`.sha256` and — when `RELEASE_SIGNING_KEY` is set — an Ed25519 `.sig`. Tag builds fail closed without
that secret (`go run ./cmd/sign-release -gen` prints the seed + the public key for
`crypto.update_signing_key`). The release notes lift the `## [<version>]` section from `CHANGELOG.md`;
keep that heading in sync with `VERSION` or the job fails.

## Misc
- `scripts/setup-dev.sh` generates `config.yaml` (port 8000).
- `scripts/gitleaks-pre-commit.sh` runs a gitleaks scan pre-commit.
- Frontend source lives under `frontend/src/pages/` and `frontend/src/lib/`; i18n strings in `frontend/src/lib/i18n/en.ts` and `zh.ts`.