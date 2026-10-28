# When a task is retried automatically

A failed task can be re-issued **once**, automatically — but only if it is a **read-only enumeration**. This page lists exactly
which types qualify and why the rest never will.

## The rule

- **Only enumerations.** `ps`, a directory listing, `whoami` — the answer is the same whenever it arrives, so asking twice
  costs nothing.
- **Only once.** A task that failed twice is a **finding**, not a scheduling problem: retrying it again would hide that.
- **Never anything that changes state.** A `shell` command, a persistence install or a credential dump **may have
  half-succeeded** before the timeout, and re-issuing it is how **one action becomes two**.

The allowlist is a code constant (`autoRetryableTaskTypes` in `internal/server/task_auto_retry.go`), not configuration:
enabling retries for a mutating type is a decision with a blast radius, and it should take a code change and a test, not a
checkbox.

## What qualifies

The list is deliberately short. Any type not named here is **not** retried:

| Group | Task types |
|---|---|
| Process and system | `ps`, `process_list`, `sysinfo`, `hostinfo`, `env`, `drives`, `whoami` |
| Filesystem (read) | `ls`, `files_ls` |
| Network (read) | `netstat`, `arp` |
| Accounts (read) | `users`, `net_user` |

## How an operator sees it

- **Task detail** carries `retry_count` when it is above zero, next to `failure_reason` — *"this result is the second
  attempt, and here is why the first one failed"*.
- **The task export** can include the column (`?fields=retry_count`, or the `full` preset); it is not in the default
  nine-column CSV, because a consumer parsing that file should not gain a column it did not ask for.
- **The failure report** (`GET /api/stats/tasks/failures`) counts `retried` in the window. A window with many retries points
  at the **link** rather than the task, which the list of reasons cannot show — every retried task ended up somewhere else in
  that report.
- **The audit log** records `task_auto_retry` per re-issued task, so *"this ran twice"* is explainable after the fact.
