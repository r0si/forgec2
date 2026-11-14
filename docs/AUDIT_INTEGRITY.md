# Audit log integrity

The audit log is a hash chain. Every entry carries `entry_hash` (a SHA-256 over its
own content) and `prev_hash` (the previous entry's `entry_hash`), so an entry that
was edited, removed or reordered no longer lines up. This page is the operator
runbook: what the chain proves, what it does **not**, and how to use the endpoints
that verify, export and anchor it.

## What the chain proves — and what it does not

**Detects:** a partial edit. One row's content changed, a row deleted, a link
rewritten — the shape an operator or a compromised account produces through the
console or a DB client.

**Does not detect:** an attacker with write access who recomputes every hash from
scratch. The hash is unkeyed and its input is documented in the code
(`auditEntryHash`), and `prev_hash` is not part of the entry hash. Defeating a full
rewrite needs a copy outside the database — that is what the anchor file and the
export are for.

Stated plainly because an over-claim here is worse than no chain: it invites people
to trust a number that does not mean what they think.

## Endpoints

All of these require the `audit:read` permission.

| Endpoint | What it answers |
|---|---|
| `GET /audit/verify?limit=N` | Walk the newest N rows: does every `entry_hash` match its content, and does every `prev_hash` name its predecessor? Answers **200 with `ok:false`** on failure — a finding, not a server error. |
| `GET /audit/export?limit=N` | The chain as NDJSON: every hashed field plus both hashes, so a verifier outside this process can recompute it. |
| `GET /audit/export?since_id=N` | Only rows with a greater id. Use it to extend a copy you already verified; the first row's `prev_hash` links back to your anchor. |
| `GET /audit/anchor` | The newest row's `id`/`entry_hash` and whether the server-side anchor file agrees (`anchor_matches`). Paste it into a report or a ticket. |
| `GET /ready` | `checks.audit_chain`, verified on **every** poll, plus a `warnings` entry on failure. |
| `GET /api/v1/health` | Same verdict, cached for 60 s (the console polls it). |

## The anchor file

Each committed audit batch appends the new head to `data/audit-anchor.log`
(append-only, `0600`). It is what catches a whole-table rewrite: the table can be
made internally consistent, the anchor file cannot be matched without also editing
it — and it does not travel with a database backup.

`anchor_matches:false` from `/audit/anchor`, or "chain head does not match the
anchor file" from `/verify`, means the table and the anchor disagree. Treat it as
an incident, not a bug report.

## When the write path is unavailable

Audit batches are written by a single worker. When its queue is full the writer
waits briefly for room and then **parks the batch on disk** under
`<data_dir>/audit-spill/`, so the request path never waits on the database and no
row is lost. If the database itself cannot be written (unreachable, full disk),
the batch is parked the same way and written on the next tick or after a restart;
the file is removed only once the rows are in the database, so a crash costs a retry
and not a row. The chain is intact either way: every write re-bases onto the
database head inside its own transaction.

`GET /ready` publishes `checks.audit_spill_pending` (batches waiting on disk) and
`checks.audit_spilled` (entries parked since the process started), and warns while
the backlog is non-empty. `checks.audit_dropped` counts the entries that could be
neither written nor parked — the only true losses.

## A practical workflow

1. **Before an engagement.** Export the chain (`/audit/export`) and record
   `/audit/anchor`. Hand both to whoever holds the engagement paperwork.
2. **During.** Periodically re-fetch with `since_id=<last id you hold>` and append
   to your copy. This is incremental, so it stays cheap on a long operation.
3. **After.** Run `/audit/verify`. If it fails, the reported `first_bad_id` and
   `reason` name the first row that does not line up; the export lets a third
   party reproduce the finding without access to the server.

## Purging

`POST /settings/purge/audit` deletes rows older than the window (floor: 90
days) and **writes an `audit_purge` marker first**, naming the row count and the
window — destroying history is exactly the operation that must describe itself.

A purge deliberately removes the oldest rows, which breaks the previous-hash link
at the new head. `/verify` accepts that discontinuity **only** at the start of the
window and only when the id sequence has a gap (proof that rows were removed);
a gap in the middle is still reported. The marker keeps the "who deleted what" part
of the record intact.

## Reporting a finding

Include the verdict (`ok`, `checked`, `first_bad_id`, `reason`), the chain head
from `/audit/anchor`, and the matching exported rows. That triple is enough for
someone else to confirm the chain was intact up to the head you recorded, without
trusting your server.
