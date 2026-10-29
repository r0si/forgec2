# Writing an automation rule

A rule is configuration an operator writes once and then trusts. That is why every rule here either works or **says why it cannot** — there
is no state in which a rule looks fine and silently does nothing.

## Events a rule can listen for

| Event type | Fires when | Payload fields a condition may use |
|---|---|---|
| `implant.checkin` | a beacon checks in | `agent.hostname` |
| `implant.disconnect` | a beacon goes offline | `agent.hostname` |
| `task.complete` | a task finished successfully | `task.id` |
| `task.fail` | a task finished badly | `task.id` |
| `credential.found` | a credential was harvested | — |
| `alert.triggered` | an alert rule fired | — |
| `build.failed` | a payload build failed | `build_id`, `platform`, `format`, `filename`, `error`, `hint_code` |
| `build.succeeded` | a payload build finished | `build_id`, `platform`, `format`, `filename` |

The list above is checked against the event registry in code, so a new event type cannot appear without a row here.

## Addressing a payload field

Two spellings resolve, and they resolve to the **same value**:

- `hint_code` — the bare key, which is how the **live** event stores it;
- `data.hint_code` — the prefixed form the **preview** used to accept only.

Both work in both paths on purpose: a rule that fires in production must not preview as "would not fire", because that teaches an
operator to rewrite a rule that is already correct.

`agent.hostname` is resolved from the event's own fields rather than from its payload.

## Operators

| Operator | Meaning | Notes |
|---|---|---|
| `contains` | case-insensitive substring | |
| `equals` | exact, case-sensitive | |
| `regex` | Go regular expression | a **pattern that does not compile matches nothing** |

Anything else — `equal`, `==`, `startswith`, an empty string — is **refused when the rule is saved or imported**, and can never match at
runtime.

## Failing closed, and why

Three inputs used to match **everything**, and each of them is now a non-match or a rejection:

- an **unrecognised operator** (a typo made a rule fire on every event of its type);
- an **empty or whitespace-only field**;
- a **key the event does not carry** — an absent key is not an empty value, and `contains ""` is always true.

The reasoning is the same in all three cases: a rule that fails open looks like it works, and the operator's next conclusion is that the
system is fine when it is not. Matching therefore **fails closed**: where the old behaviour defaulted to "match", it now defaults to "no
match", and a definition that would have relied on that default is refused before it can be saved.

## Finding a rule that cannot fire

A rule can become unfirable **after** it was accepted: validation arrives later than some stored rules. Three surfaces catch it:

| Surface | What it tells you |
|---|---|
| `GET /api/automation/health` | `invalid_condition` (severity **error**) naming the operator and the allowed set |
| `POST /api/automation/preview` | *"this condition can never match, so the rule cannot fire"*, distinct from *"did not match"* |
| `GET /api/automation/rules/{id}/preview` | the same distinction for one rule |

`invalid_condition` is an **error**, not a warning: a rule that cannot fire is broken, not merely quiet.

## Trying a rule before trusting it

In order of increasing commitment:

1. `POST /api/automation/preview` — every rule of the tenant against a synthetic event; executes nothing.
2. `POST /api/automation/diagnose` — what fires **and** who would be told; executes nothing.
3. `POST /api/automation/rules/{id}/test` — **runs the rule's actions** (`confirm: true`, 10 s per-rule cooldown, audited).

The first two answer "would this happen". The third is the only one that does anything, which is why it costs a confirmation.
