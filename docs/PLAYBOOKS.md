# Playbooks: the state machine

`internal/playbook` is the rule set behind structured playbooks — a list of steps, their dependencies, and the transitions that
stop a run from claiming progress it did not make. It is **pure**: no database, no clock, no network. Storage and execution belong
to the layers above, which is what makes the transitions testable exhaustively instead of by inspection.

## States

| State | Means |
|---|---|
| `pending` | may become runnable once its dependencies succeed |
| `blocked` | waiting for a **human**; an approval step starts here, so "waiting on a person" is a state a run can report |
| `running` | dispatched |
| `succeeded` | finished, and satisfies its dependents |
| `failed` | finished badly; dependents are **skipped**, never silently treated as satisfied |
| `skipped` | not run, because a dependency failed or was itself skipped; `skipped_because` names it |

## The rules that matter

- **Only `succeeded` unlocks a dependent.** `running` is not done — the transition that treats "started" as "finished" is how a
  playbook reports success for work that never landed.
- **An approval step cannot be jumped.** `Approve` releases the step only if its dependencies already succeeded, so approving early
  does not reorder the run.
- **Failure cascades in the same transition.** `Fail` skips everything downstream and records the blocker, because a run that leaves
  dependents `pending` after a failure advertises work that is no longer possible.
- **`Progress` counts settled steps**, including skipped ones: a decided branch is finished, not outstanding.
- **`State` answers with the most urgent word first** — `failed`, then `blocked`, then `running`, then `partial`, then `complete`.
  A reviewer needs "this failed" before "this could continue".
- **`Normalise` never resets a terminal status**, which is what makes resuming from storage work: the run keeps what it knew, and
  re-validates its own graph.

## Refused definitions

`Normalise` rejects a playbook that can never finish: no steps, empty or duplicate ids, an unknown dependency, a self-dependency,
and any cycle. Each of those produces a run that looks fine in a list and is dead on arrival; the cycle is reported at the **same
step on every run** (ids are visited in sorted order) so two diagnoses of one playbook agree.

## What is deliberately absent

No scheduler, no retry policy, no approval UI. Those are the layers above: this package answers *"what may run, and what happened"*,
nothing else. Retries in particular are a policy decision with a blast radius — the same reasoning that keeps automatic task retries
limited to read-only enumerations.

## The endpoints around it

`internal/server` stores runs and records transitions. The routes:

| Endpoint | Answers |
|---|---|
| `GET /api/playbooks/runs` | the runs, newest first, with `settled`/`total` and the two columns that matter: **`blocked_steps`** and **`next_step`** |
| `GET /api/playbooks/runs/{id}` | one run: every step, its status, and the computed `ready` set |
| `POST /api/playbooks/runs` | record a run (steps are normalised and cycle-checked; a refused definition is a 400) |
| `POST /api/playbooks/runs/{id}/transitions` | move one step — `approve`, `start`, `succeed`, `fail`, `skip` |
| `GET /api/playbooks/runs/{id}/history` | the run's audited transitions, oldest first |

### `blocked_steps` and `next_step` exist because "blocked" is ambiguous

A blocked **approval** is a request for an operator. A blocked **dependency** is a run that will unblock itself. Collapsing them into one
word is how a queue of pending approvals goes unnoticed, so the list carries the distinction — and `?attention=1` keeps only the runs that
want a human, because a caller that has to filter that itself usually will not.

### A run can die two ways, and both are named

A blocked approval is the obvious one. The other is a step that was **started and never closed**: the state machine is right to call the
run `running`, so nothing is wrong and nothing draws attention either. The list therefore carries `running_steps` and a **`stalled`** flag
for a running step untouched for **12 hours** — long enough that finishing after lunch is normal, short enough that a checklist stopped
yesterday is visible. `?attention=1` keeps stalled runs alongside the ones waiting on an approval, for the same reason the queue report
names a beacon that is not draining its tasks.

### A transition is refused, not corrected

A transition the state machine refuses answers **409** with the reason. It is not applied partially and not reported as success: a
checklist that accepts `succeed` on a `pending` step is a checklist that records things that never happened.

### Nothing here dispatches anything

No endpoint above starts a task, a listener or a beacon. Every write response carries a `note` saying so. Wiring an executor to this state
machine is a separate decision with its own blast radius: a playbook that can run commands is an automation engine, and the approval step
exists precisely because the machine is not trusted to decide alone.

### Reading it

- `GET /api/playbooks/runs?attention=1` — what needs a person right now.
- `GET /api/playbooks/runs/{id}` — what may run next, and why not.
- `GET /api/playbooks/runs/{id}/history` — who advanced it, and when. The rows are the audit log's, and each carries `entry_hash` so it can
  be matched against `/api/audit/export`; this is a convenience view, not a second record.
