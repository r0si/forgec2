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

### What the rule-health codes mean

`GET /api/automation/health` returns findings with a stable `code`, so an operator can filter them without parsing prose. The
codes are the vocabulary of the whole rule feature — a rule that "looks fine" and never fires is the failure this report
exists to catch.

| Code | Severity | Means |
|---|---|---|
| `unconditional_mutating_action` | error | the rule has **no conditions** but performs an action that changes something: every event of its type runs it |
| `missing_agent` | error | an action targets an agent id that **does not exist** here |
| `missing_script` | error | an action targets a script id that does not exist here |
| `missing_macro` | error | an action targets a macro id that does not exist here |
| `target_unavailable` | warning | the agent exists but **cannot accept tasks** — past its deadline, its engagement closed, or its window ended |
| `duplicate_rule` | warning | two enabled rules fire on the same event with the same conditions; the second is usually an accident |
| `action_failing` | error | the rule's actions have been dispatched and **never landed**; the last error is reported beside it |
| `never_fired` | warning | enabled, and no event has matched it in the grace period — usually a mistyped condition |
| `firing_often` | warning | it fires far more than a human-scaled rule should; a loop or an over-broad matcher |

Severity is about **what to do first**, not about correctness: `never_fired` and `firing_often` are warnings even though they
are the two most common ways a rule quietly does nothing.

### What the failure reasons mean

`GET /api/stats/tasks/failures` groups by a `reason` code derived from the error text. The classification is **first-match on
substrings**, most specific first, and it is a starting point for investigation rather than a verdict — the raw error travels
beside it, and anything unmatched is collected as `other` with an example rather than dropped.

| Reason | Matched on | Usually means |
|---|---|---|
| `timeout` | timeout, timed out, deadline exceeded | the beacon did not answer in time — check the link before the command |
| `access_denied` | access denied, permission denied, privileges, 401/403 | the host refused: wrong privilege level, not a broken task |
| `defender` | defender, antivirus, AMSI, quarantine | an endpoint-protection product interfered |
| `not_found` | not found, no such file, does not exist | the path or binary is not there |
| `network` | connection refused/reset, unreachable, broken pipe | the connection failed in transit |
| `dns` | no such host, name resolution | resolution failed before any connection |
| `tls` | TLS, certificate, x509, SSL | a certificate or handshake problem |
| `unsupported` | unsupported, unimplemented, unknown command, invalid task type | the agent does not implement what was asked — often a version mismatch |
| `disk` | no space, disk full, read-only file system | the write could not land |
| `cancelled` | cancelled/canceled, aborted, killed | someone or something stopped it; not a failure of the task |

Two of these are worth reading twice: `cancelled` is not a fault, and `unsupported` usually means the **agent binary is older
than the console**, which no amount of retrying fixes.

## Two conventions every report follows

**Scope: `?engagement=<id>` or `?engagement=none`.** The failure report, the failure-shape view and the duration report all
take it, resolved through the tenant scope, so "what went wrong in **this** exercise" cannot silently answer a different
question. `none` is the inverse question an operator needs: hosts no deadline will ever stop. An unusable value is a **400**,
never a filter that quietly selects everything.

**Self-description: every export names what it contains.** A filtered file looks exactly like an unfiltered one, so:

| header | on | says |
|---|---|---|
| `X-Task-Export-Scope` | task CSV/JSON | the filters that shaped the rows, sorted, or `all` |
| `X-Task-Export-Truncated` | task CSV/JSON | whether the row cap cut the answer |
| `X-Audit-Export-Scope` | audit NDJSON | the same, for the chain export |
| `X-Audit-Export-Filtered` | audit NDJSON | whether the file is a selection (and therefore **not** a contiguous hash chain) |

`GET /api/exports/capabilities` publishes these names, so a client reads them from the API rather than from this page.

## Act: the endpoints with side effects

| Endpoint | What it does |
|---|---|
| `POST /api/automation/rules/{id}/test` | **Runs the rule's actions** against a synthetic event (`confirm: true` required, 10 s cooldown) |
| `POST /api/notification-routes/{id}/test` | **Delivers a real message** to the configured target (disabled route: 409, 10 s cooldown) |
| `POST /api/tasks/rerun-by-signature` | **Re-dispatches** the failed tasks that share one shape (bounded to 100) |

`/api/exports/capabilities` marks the first two with `side_effects: true`, so a client offering "run all diagnostics" can exclude
them without guessing — and the test that enforces that requires **exactly** that set.

### Reruns are idempotent, and that is why a double click is safe

A rerun does not invent a new identity: it carries the original task's **idempotency key**, and an unkeyed original gets a
deterministic `rerun:<id>` key derived from its own id. So a button pressed twice re-submits the **same** key and the queue
returns the row it already created instead of stacking a duplicate — which matters most exactly where this endpoint is used,
on a bulk action over dozens of failures. The response reports those as `already queued` in `skipped`, with the reason stated,
so a `queued` count lower than `matched` is explained rather than mysterious.

The same protection is what makes the bound meaningful: `limit` (default and maximum 100) caps how much **new** work one call
can create, not how many times a client may ask.

## Clear: making a fixed problem stop looking broken

| Endpoint | What it clears |
|---|---|
| `POST /api/notification-routes/stats/reset` | Delivery counters for one route, or all |
| `POST /api/automation/action-stats/reset` | Action counters for one rule, or all |

Both are audited. An indicator that can only get worse is one operators learn to ignore.

## The exit-code contract for the offline verifiers

`cmd/verify-bundle`, `cmd/verify-audit` and `cmd/verify-plugin` share one contract — `0` matches, `1` wrong, `2` unreadable,
`3` intact-but-unproven, `4` intact-but-incomplete — documented in `docs/VERIFIERS.md` rather than repeated here.
