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
