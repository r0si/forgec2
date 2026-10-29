# Diagnostics and observability

The endpoints that answer questions and change nothing — plus the few that do change something, kept apart on purpose. Every
path on this page is checked by a test against the router, so a renamed endpoint cannot leave this page lying.

## Preview: what would happen

| Endpoint | The question it answers |
|---|---|
| `POST /api/automation/rules/{id}/preview` | Why would **this** rule fire, or why did it not? |
| `POST /api/automation/preview` | Which rules of this tenant would fire for one event? |
| `POST /api/automation/diagnose` | What fires for this event **and** who would be told? |
| `POST /api/notification-routes/preview` | Would this notification reach anyone, and which route rejects it? |

All four **execute nothing**. Each is audited, and using one costs no side effect beyond the audit row.

## Inspect: what is happening

| Endpoint | The question it answers |
|---|---|
| `GET /api/automation/health` | Which rules are unconditional, dangling, duplicated, never fired, or firing into a void? |
| `GET /api/stats/tasks/failures` | Why do tasks fail, grouped by reason, type, agent or engagement? |
| `GET /api/stats/failures/signatures` | What **shape** of failure dominates — four 401s or one expired token? |
| `GET /api/plugins/failures` | Which packages are on disk but were rejected at load, and why? |
| `GET /api/exports/capabilities` | What does the download and diagnostics surface accept? |
| `GET /ready` | Is the process healthy — and are delivery, actions, retries, plugins? |

## Act: the endpoints with side effects

| Endpoint | What it does |
|---|---|
| `POST /api/automation/rules/{id}/test` | **Runs the rule's actions** against a synthetic event (`confirm: true` required, 10 s cooldown) |
| `POST /api/notification-routes/{id}/test` | **Delivers a real message** to the configured target (disabled route: 409, 10 s cooldown) |
| `POST /api/tasks/rerun-by-signature` | **Re-dispatches** the failed tasks that share one shape (bounded to 100) |

`/api/exports/capabilities` marks the first two with `side_effects: true`, so a client offering "run all diagnostics" can exclude
them without guessing — and the test that enforces that requires **exactly** that set.

## Clear: making a fixed problem stop looking broken

| Endpoint | What it clears |
|---|---|
| `POST /api/notification-routes/stats/reset` | Delivery counters for one route, or all |
| `POST /api/automation/action-stats/reset` | Action counters for one rule, or all |

Both are audited. An indicator that can only get worse is one operators learn to ignore.

## The exit-code contract for the offline verifiers

`cmd/verify-bundle`, `cmd/verify-audit` and `cmd/verify-plugin` share one contract — `0` matches, `1` wrong, `2` unreadable,
`3` intact-but-unproven, `4` intact-but-incomplete — documented in `docs/VERIFIERS.md` rather than repeated here.
