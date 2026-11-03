# Frontend API paths

Source of truth: `frontend/src/lib/api-paths.ts`.

## Rules

1. **JSON lists** use `/api/...` (e.g. `paths.agents.list()`, `paths.listeners.list`).
2. **Agent detail / commands** use `/agents/:id/...` (backend layout; not all under `/api/agents`).
3. **Dual-use** routes serve SPA HTML to browsers and JSON when `Accept: application/json`:
   - `GET /loot` — loot page / screenshot collection data
   - Some `/agents/:id` pages
4. Do **not** invent `/api/loot` unless the Go router adds it.
5. CI: `npm run check:paths` fails on:
   - bare list paths like `api.get("/agents?...")` (must use `/api/...`)
   - static string paths with unknown prefixes (must use `paths.*` or extend ALLOW_PREFIX)

## Quick map

| Resource | Path helper |
|----------|-------------|
| Agent list | `paths.agents.list()` |
| Agent one | `paths.agents.one(id)` |
| Agent tasks | `paths.agents.tasks(id)` |
| Listeners | `paths.listeners.list` |
| Credentials list | `paths.credentials.list()` → `/credentials` (dual-use, JSON via `Accept`) |
| Credentials by agent | `paths.credentials.byAgent(id)` |
| Credentials mutations | `paths.credentials.add` / `one` / `confirm` / `batchTags` → `/credentials/*` |
| Listeners CRUD | `paths.listeners.list` / `one` / `enable` / `disable` |
| Profile import | `paths.generate.profileImport` |
| Payload builds | `paths.generate.binary(fmt)` / `donut` / `oneLiner` / `delivery` |
| Chain graph | `paths.chain.graph` → `/chain/graph` |
| Config hot reload | `paths.config.reload` |
| Users (dual) | `paths.users.list` → `/users` (not `/api/users`) |
| Builds (dual) | `paths.builds.list()` → `/builds` (not `/api/builds`) |
| Settings (dual) | `paths.settings.root` → `/settings` |
| Audit logs | `paths.audit.logs(q)` → `/audit/logs?...` |
| Notifications | `paths.notifications.list()` → `/notifications` (no `/api`) |
| Groups | `paths.groups.list` → `/groups` (no `/api`) |
| Loot (dual) | `paths.loot.page` |
| Loot bulk delete | `paths.loot.bulkDelete` |
| Agent batch | `paths.agents.batch` / `bulkTask` / `batchDelete` |
| Agent cmd builder | `paths.agents.cmd(id, "shell")` → `/agents/:id/shell` |
| Agent files/token/socks | `paths.agents.filesLs` / `filesSearch` / `filesScan` / `filesThumb` / `filesThumbGet` / `tokenList` / `socks` / … |
| Agent remote input | `paths.agents.remoteInput(id)` → `/api/agents/:id/input` |
| Agent live desktop | `paths.agents.webrtcStart(id)` / `webrtcStop(id)` → `/agents/:id/webrtc/*`; signaling sockets are `/ws/rd/operator?session=` (operator) and `/ws/rd/agent?session=&token=&agent=` (agent, single-use token) |
| Campaigns | `paths.campaigns.list` / `one` / `mitre` |
| SOCKS sessions | `paths.socks.sessions` |
| RPort status | `paths.rportfwd.status` |
| OPSEC / Privesc / Lateral | `paths.opsec.*` / `paths.privesc.*` / `paths.lateral.*` |
| Phishing / Scheduler / Scripts | `paths.phishing.*` / `paths.scheduler.*` / `paths.scripts.*` |
| Automation / Plugins / AI | `paths.automation.*` / `paths.plugins.*` / `paths.ai.*` |
| BOF / BloodHound | `paths.bof.*` / `paths.bloodhound.*` |
| Chat / Circuit breaker | `paths.chat.*` / `paths.circuitBreaker.*` |
| Autotag / Domain front / Mesh | `paths.autotag.*` / `paths.domainFront.*` / `paths.mesh.*` |
| Settings mutations | `paths.settings.agent` / `server` / `malleable` / `certs*` / `sync*` / `siem*` |
| Roles / Tags | `paths.roles.*` / `paths.tags.list` |

**Goal:** application source should not call `api.*( "/…")` with string literals — only via `paths.*` (tests may use stubs).
| Workflows | `paths.workflows.list` / `one` |
| Report overview | `paths.report.overview` → `/report` |
| Report sections | `paths.report.agents/tasks/...` → `/api/report/*` |
| Dashboard | `paths.dashboard.v1` → `/api/v1/dashboard` |
| Generate profiles | `paths.generate.profiles` |

Envelope helpers (`lib/envelope.ts`): use `normalizeListEnvelope` / `firstField` / `firstNumber`
for dual-use PascalCase/snake_case payloads (`users`/`Users`, `logs`/`Logs`, etc.).

## Live Playwright (optional)

```bash
# Against running forgec2-server on :8000
FORGEC2_E2E_BASE=http://127.0.0.1:8000 FORGEC2_E2E_PASS='Admin123!' npm run test:e2e -- e2e/live.spec.ts
```

When `FORGEC2_E2E_BASE` is unset, `live.spec.ts` is skipped (static `smoke.spec.ts` still runs).
