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

A local `npm run build` regenerates `frontend/out` from the **working tree**. If anyone has
uncommitted frontend changes, `out/` stops matching the committed
`internal/webdist/dist` and `check:webdist` fails — that failure means "out is not the
committed bundle", not "dist is stale". Restore it by copying the committed bundle back
(`Remove-Item -Recurse -Force frontend/out`, recreate it, then
`Copy-Item -Recurse -Force internal/webdist/dist/* frontend/out/`), and do **not** commit a
dist built from a tree with someone else's work in it.

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
  setter, the rate-limited phishing landing) are listed with a reason in the script, as are the
  implant-facing endpoints that authenticate a beacon instead of a session. It scans **every**
  server file, not just `routes*.go`: mutating routes registered in `server.go`, `api.go` or a
  `handler_*.go` `register*` function were invisible, and `POST /api/dns/start` (which starts a
  listener on :53) sat on the global auth group with no permission check because of it. Its
  GATE_MARKERS are **only** authorizers: `currentUserID` (identity resolution) and the scoping
  helpers (`tenantScope`, `findTenantOrFail`, `getAgentOrFail`) were removed after
  `/emergency-stop` turned out to pass on identity alone, and the script now refuses to accept any
  of them as a gate — proven non-load-bearing, since all 402 mutating routes still pass without
  them.
- `check:context-keys` (`scripts/check-context-keys.mjs`) — a gin context key read
  with `c.Get*` must be written by production code; test-only setters do not count, and `const`
  key names are resolved. "`c.Get*`" means **every context accessor**, not just `Get`,
  `GetString` and `MustGet`: `c.GetBool("auth_via_api_key")` decides whether an API key is
  scoped or a legacy full-access key, and the narrow pattern could not see that read at all.
- `check:error-sanitization` (`scripts/check-error-sanitization.mjs`) — a 5xx must
  answer a literal or `sanitizeError(...)`, never a raw `err.Error()`. It reads **both** ways a
  5xx is written — the `respondError(…)` helper **and** a direct `c.JSON`/`c.String`/
  `c.AbortWithStatusJSON` with any 5xx status or a numeric `500` — because knowing only the
  helper (and only `StatusInternalServerError`) left a **503** in the WebRTC path echoing the
  TURN resolver error verbatim.
- `check:tx-queries` (`scripts/check-tx-queries.mjs`) — no query through the pool
  handle inside a transaction block: SQLite is pinned to one connection, so the
  nested query waits forever and the handler hangs instead of erroring. The receiver may be a
  **chain with arguments** (`s.db.WithContext(ctx).Transaction(…)`): the first pattern required
  bare method names, so that whole block — it exists in `audit.go` — was never recognised as a
  transaction, and the pool handle beneath the chain is flagged too.
- `check:command-separators` (`scripts/check-command-separators.mjs`) — a function
  that joins a task command with `"|"` must validate its fields first
  (`validateCommandArg` or `credentialCommandHasSeparator`). The implant splits the
  command on `"|"`, so an unvalidated separator in one field shifts every later
  field and the task silently means something else. That shipped four times:
  password_spray re-targeted the domain, cred_check probed the wrong account **and
  bypassed the lockout fuse**, persistence removed a different entry, uac_bypass
  ran a different payload. In-memory key builders are allowlisted with a reason.
- `check:task-docs` (`scripts/check-task-docs.mjs`) — every handler registered in
  `internal/payload/agent`'s `taskHandlers` map must have a row in
  `docs/COMMAND_REFERENCE.md`; an undocumented task type is a feature nobody can
  find. One direction only, deliberately: 53 documented types are handled by the
  core dispatcher or the C implant rather than by that registry, so demanding a
  handler for every row would fail on legitimate entries. It reads **three** registration
  idioms — `taskHandlers["x"] = …`, the `"x": handleX,` map literal in `task_registry.go`,
  and `RegisterTask(protocol.MustSpec("x"), …)`; knowing only the first left the gate blind
  to **49** handlers, and `abort` was undocumented until that was fixed.
- `check:tenant-scope` (`scripts/check-tenant-scope.mjs`) — a query scoped with
  `tenantScope`/`auditTenantScope` must target a model that **has a tenant column**; those
  helpers append `WHERE tenant_id = ?`, so scoping a table without one produced
  `no such column: tenant_id` and answered **500** for every tenant-scoped operator
  (`agent_status_events` did). The model is resolved from the scope line, its statement, the
  query destination variable, or the function's only table; a call it cannot resolve is
  **reported, not guessed**, and a deliberate exception carries `// check:tenant-scope-ok`.
- `check:mitre-coverage` (`scripts/check-mitre-coverage.mjs`) — every task type named in
  the ATT&CK coverage map (`attackTacticMap` in `handlers_mitre.go`) must be a **dispatchable
  spec** in `pkg/protocol`; the map carries its own warning about "fantasy names ... which no
  agent handler implements", and a comment cannot enforce that. It also rejects a malformed
  technique id. Measured when added: 32 techniques, 28 mapped, 4 honestly uncovered, 0 unknown
  types.
- `check:runtime-paths` (`scripts/check-runtime-paths.mjs`) — a file operation in server code
  must not start from a path relative to the **process working directory**. This shape shipped
  five times with five different symptoms: hosted payloads and uploads wrote a relative
  `payloads`/`uploads` directory (files next to wherever the binary started, cleanup scanning
  another tree), the ACME handler put a TLS **private key** there, build output resolved to
  `<cwd>/agents`, `GET /api/docs/openapi.yaml` answered 500 in every release (the archives ship
  no `api/` directory), and plugin **quarantine** moved deleted sources into `<cwd>/data`. The
  implant is exempt: absolute host paths (`/etc/hosts`, `/proc/<pid>`) are the point. A
  deliberate relative path carries a reason in the script's ALLOWLIST.
- `check:owner-scope` (`scripts/check-owner-scope.mjs`) — a request handler that touches a
  table **without a tenant column** by id must scope through the owning relationship. Fourteen
  rounds of this audit found the same shape by hand (scripts, BOF blobs, phishing templates and
  campaigns, workflows, one-shot schedules, agent status history, agent tags), each time with the
  same fix and each time reachable by any operator who guessed an id. The gate knows the table →
  helper mapping, accepts any approved scope helper (including the `findVisible*` wrappers) and
  documents its known gap: `agent_groups` has neither a tenant nor a creator column yet.
- `check:nav-perms` (`frontend/scripts/check-nav-perms.mjs`) — a sidebar entry must not be gated by a
  permission its page's API does not check. The nav hides entries with `perms` the operator lacks
  ("any-of"); the keys are typed, so a typo is a compile error, but **nothing compared them to the
  backend**, and two entries disagreed: `/dns` had no perms at all while every `/api/dns/*` route
  requires `listeners.read`, and `/ai` required `settings.read` while its page handler and
  `/api/ai/runs` require `ai.use`. The built-in `user` role carries all three, so only **custom-role**
  deployments saw it — a page that answers 403 everywhere, or a page you can use but cannot reach. The
  gate parses route groups (`X.Use(RequirePermission(db.PermFoo))` plus inline overrides) and compares
  the first path segment; it **only** compares nav items whose `/api/<segment>` GET routes agree on one
  permission set, so 17 of 44 are checked and the rest are counted, not guessed. It resolves `PermFoo`
  to its string through `internal/db/models.go` after a CamelCase guess turned `PermAIUse` into
  `AIUse` and produced a false mismatch.
- `check:discarded-results` (`frontend/scripts/check-discarded-results.mjs`) — a delivery that failed must
  not be discarded. Four rounds found the same shape by hand (automation script result thrown away,
  `create_task`/`set_sleep` skipping silently, webhook response status never inspected, notification
  delivery error ignored with `_ =`), and a discarded error is invisible both at the call site and in
  the UI. It matches `_ = <receiver>.<send|deliver|dispatch|notify|push|post|webhook|execute>…(` and a
  **bare call** to a known error-returning delivery function; close/flush ignores and
  `_ = json.Unmarshal(…)` are not matched. Its allowlist is empty on purpose: the first version
  allowlisted the notification file and let a probe reintroduce the exact defect.
- `check:http-status` (`frontend/scripts/check-http-status.mjs`) — an outbound call must read the status it
  got. The notification webhook had this defect in 227 (a receiver answering 500 looked like a 200) and
  the GeoIP lookup had it one layer down: `ip-api` answers **200** with `{"status":"fail"}` and **429**
  once the free tier is exhausted, and the decoder turned both into empty fields with no explanation.
  The gate requires a `StatusCode` check within 45 lines of any `http.Client.Do` /
  `http.Get/Post/Head/PostForm`; 13 such calls currently comply, and the allowlist is empty.
- `check:guarded-clients` (`frontend/scripts/check-guarded-clients.mjs`) — the outbound-client guard has
  exactly **one** implementation. `ssrfSafeClient` and `aiSSRFClient` were the same construct written
  twice and rounds 232/233 fixed the zero-timeout hole **one copy at a time**; both now call
  `guardedClient(base, fallbackTimeout, validate)`. The gate fails if `Timeout: base.Timeout` or
  `base = http.DefaultClient` appears outside `guarded_client.go`, so a future copy cannot reintroduce
  the half-fixed state.
- `check:goroutine-recovery` (`frontend/scripts/check-goroutine-recovery.mjs`) — a **new** goroutine in a
  request path must not be able to kill the process. Go has no process-wide panic handler, and a survey of
  `internal/server` found **63** `go …` sites with no reachable recover, so the gate freezes the **13** in
  `handlers_*.go` (list is shrink-only) and refuses new ones. Two of the worst-looking sites are guarded by
  design — `wsReadPump`/`wsWritePump` handle their own errors, and the AI broker retry worker selects on
  `dst.done` so a racing unsubscribe cannot panic on a closed channel — which is why the rule is "no new
  ones", not "add recover to all 63".
- `check:test-stdio` (`frontend/scripts/check-test-stdio.mjs`) — a **test** must not close a handle it does not own.
  Round 254 found the flake that had been read as a product bug for many rounds: the log-rotation test's
  cleanup closed every `io.Closer` sink of the fanout writer, and **`os.Stdout` implements `io.Closer`**.
  Closing it cost the test binary its final `PASS` line, so `go test` reported `FAIL` **with no failing
  test named** and verbose output that stopped mid-run — and it hid a real regression (a rerun answering
  400 instead of 429) for several rounds. The gate flags a `for … range *.sinks` loop that calls
  `Close()` without skipping `os.Stdout`/`os.Stderr` **inside the loop body** (the first version checked
  the whole file and passed a reintroduced defect). Scope is stated on purpose: it catches the construct
  that shipped, not every way to misuse a handle.
- `check:docs` (`gen-command-reference.mjs --check` + `gen-capability-matrix.mjs --check`) —
  the generated docs must match their sources: the command reference, and the **generated
  task-inventory block** plus the version stamp in `docs/CAPABILITY_MATRIX.md`. CI runs these
  in a dedicated step; they were **not** in either local task list until now, so a stale doc
  passed `npm run check` locally and only failed in CI. Scope worth knowing: the hand-written
  per-OS tables in that file (Yes/No/Partial cells) are **not** verified — only the block
  between the `BEGIN/END GENERATED TASK INVENTORY` markers and the `as of vX.Y.Z` stamp.
- `TestSuccessEnvelope` / `TestErrorEnvelope` (Go) — ratchets over raw (non-envelope)
  JSON success and error responses, per file, failing in both directions: a new raw
  response, or a file that improved without lowering its number. Update the matching
  `internal/server/envelope*_baseline.txt` in the same commit.

## Tables without a tenant column

`tenantScope` appends `WHERE tenant_id = ?`, so it can only be used on a model that HAS the
column. Several tables deliberately (or historically) do not, and scoping them has to run
through a relationship instead:

| table | scope through |
|---|---|
| `agent_status_events`, `one_shot_tasks` | the **agent** (`agent_id IN (SELECT id FROM implants …)`) |
| `agent_tags`, `agent_groups`, `auto_tag_rules` | their own **`tenant_id`** column (added in round 200; tag names are unique per tenant) |
| `scripts`, `bof_files`, `phishing_templates`, `phishing_campaigns`, `workflows` | the **uploader/creator** via `creatorScope` (`created_by IN (SELECT username FROM users WHERE tenant_id = ?)`) |
| `ai_attachments`, `ai_chat_sessions` | the **owner column** plus the tenant on the row |
| `custom_roles` | **not scoped** — global RBAC config, and create/update/delete are admin-only |

`findOrFail(c, &row, id, name)` takes the context and **ignores it** for scoping — it is a plain
`First` by id. It now flags nothing, which is why every caller must think about the table. The
`check:tenant-scope` gate covers the models that DO have the column; the remainder are tracked
here so the next change does not assume they are isolated.

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

## Recurring defect shapes, and where the guard lives

Nine audited rounds found the same few shapes over and over. Each now has a test or a
gate, so the point of this section is: **when you touch one of these areas, the guard
tells you if you break it — do not delete the guard to make a change pass.**

| shape | what it looked like | guard |
|---|---|---|
| "reports success, nothing happened" | `Load` applied 21 `FORGEC2_*` vars, the reload path applied none; `CopyFrom` skipped four sections; reload reported a token it had no handler for | `TestReloadKeepsEnvironmentOverrides`, `TestCopyFromCoversEverySection`, `TestReloadTokensMatchBetweenDiffAndTable`, `TestDiffConfigHasNoDuplicates` |
| config silently dropped or deleted | a misspelled key was ignored; a console save rewrote the file and deleted keys the build did not know | `TestUnknownConfigKeysFindsTypos`, `TestSavePreservesUnknownKeys`, `TestShippedExampleConfigHasNoUnknownKeys` |
| a filter plus a sort the index cannot serve | `WHERE tenant_id = ? ORDER BY created_at DESC LIMIT 50` read every row of the tenant and sorted in a TEMP B-TREE | `TestHotQueriesHaveNoUnindexedSortOrScan`, `TestListPageIndexesExist` |
| a comment that describes the opposite of the code | "walks the newest rows" while querying `ORDER BY id ASC LIMIT n` — the verifier re-checked ancient history forever | `TestAuditVerifyChecksTheNewestWindow`, `TestAuditVerifySeesDeletionNearTheHead` |
| a guarantee that lived only in a comment | "each chunk is HMAC-chained", but the push chain was keyed per chunk so every chunk restarted from the zero seed | `TestPushChainCarriesAcrossChunks`, `TestPushChainCatchesReordering` |
| a failure path that un-did its own protection | a rejected chunk reset the chain, so the same chunk matched again (4 of 8 concurrent duplicates accepted) | `TestFileChainRejectsReplay`, `TestFileChainConcurrentDuplicateOnlyOneWins` |
| a local failure treated as tampering | a disk error advanced the chain, poisoning the transfer permanently | `TestChainRollbackAfterLocalFailure`, `TestRollbackClearsPoison` |
| a lookup by id with no tenant or visibility check | bulk history, build jobs, approval prompts | the tenant tests in `bulk_history_tenant_test.go`, `build_job_tenant_test.go`, `approval_expiry_test.go` |
| a hot path re-doing work per request | profile JSON re-read and re-parsed per beacon; the transform chain re-parsed per decode | `BenchmarkActivePlacements`, `BenchmarkParseWireCached`, `TestActivePlacementsCacheIsUsed` |

Two habits produced all of these, and both are cheap:

- **Measure before claiming.** The clean example is the task-list index: the
  whole-handler benchmark was noisy and showed nothing, while the isolated query went
  983µs → 555µs. Reporting the handler number would have been a story.
- **When a guard fails on your change, first ask whether the guard is wrong.** Three times
  the answer was "the code is right, my expectation was not" (the placements staleness
  bound, the manager quota default when the config says 0, and a test that demanded
  instant profile refresh); once it was "the guard is right, the code is wrong" (the
  concurrent-duplicate test that exposed the poisoned-transfer bug). Read the real values
  before editing either side.

## Coverage floors are ratchets

Both sides carry a coverage floor, and **a floor that is never raised stops guarding anything**:

| side | floor | measured (2026-10-28) | where |
|---|---|---|---|
| Go | **51.5%** | 52.8% (was 50 at a 50.4 measurement — a 2.8-point regression would have passed) | `.github/workflows/ci.yml` "Coverage floor" |
| frontend | **33.5%** | 34.84% statements / 23 313 | `frontend/scripts/check-coverage.mjs` (override with `COVERAGE_FLOOR`) |
| first-paint bundle | **900 kB** | 798 kB critical path (4.54 MB total under a 5.5 MB cap) | `frontend/scripts/check-bundle.mjs` |

Raise a floor in the same change that adds tests; lower it only as part of that change, never to make
a regression pass. ~1 point of headroom is deliberate: it absorbs ordinary page work while a large
new component shipping without tests still trips the check. The frontend floor had drifted 18 points
below the measured figure, which meant roughly half the suite could be deleted unnoticed.

The **bundle budget is the same kind of ratchet**: it sat at 1090 kB while the critical path fell to
798 kB, so the "~122 kB of headroom" its comment promised had quietly become 292 kB — enough for a
few-hundred-kB route chunk to re-leak and still pass. It is now 900 kB. **When a budget's comment
states a headroom, check that arithmetic against the current measurement.**

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

## AI plugin authoring

The assistant can own a plugin end to end, and the four tools are the whole surface:

| tool | permission | risk | what it does |
|---|---|---|---|
| `create_plugin` | `plugins.write` | write (approval) | writes `manifest.yaml` + entry, validates, registers; **never runs** it |
| `run_plugin` | `plugins.execute` | write (approval) | runs a **command** plugin through `Manager.ExecuteCommand` |
| `update_plugin` | `plugins.write` | write (approval) | rewrites the entry of an **assistant-authored** package, patch-bumps the version, re-registers |
| `delete_plugin` | `plugins.delete` | destructive (approval) | unregisters: unload + record + package directory |

Rules that make this safe, and the reasons they exist:

- **The ABI is one JSON object on stdout** — read the input JSON from stdin, then print
  `{"success":true,"output":"..."}` or `{"success":false,"error":"..."}`. Plain text is a **parse
  error, not a result**, which is why both tool descriptions and the default system prompt state it;
  the first version of the prompt only said "write your result to stdout" and every generated plugin
  failed with `failed to parse plugin result`.
- **Writing is not running.** `create_plugin` and `update_plugin` never execute the package; running
  needs `plugins.execute` and (in the console) a human clicking Run. Generated code is code, and code
  is reviewed before it runs.
- **A model may only modify or delete its own output** (`author == "forgec2-ai"`). The installed set is
  the operator's inventory and a model cannot see what depends on an entry it did not write.
- **`Manager.Unregister` deletes the package directory and the DB record**, so an in-place update reads
  the old files into memory first, then unregisters, recreates, registers, and recreates the record
  (`FirstOrCreate`, not `Update`). Writing new files first and unregistering second leaves `Register`
  loading a manifest whose files were just removed — that shipped once and showed up as `plugin failed`
  only when the updated plugin was run.
- **A failed call removes only what it created.** A duplicate name is refused *before* the directory is
  created; otherwise the cleanup path deletes the plugin that already exists (this also shipped once).

`TestAICreatePluginTool`, `TestAIRunPluginTool`, `TestAIUpdatePluginTool`, `TestAIDeletePluginTool` and
`TestDefaultPromptTeachesPluginAuthoring` pin these; the last one exists because the prompt is part of
the feature — tools the model does not know about are tools it will not use.

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