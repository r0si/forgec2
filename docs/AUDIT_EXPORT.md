# Verifying an audit export

The audit log is a hash chain: every row carries `entry_hash`, the digest of its own fields, and `prev_hash`, the digest
of the row before it. `GET /api/audit/export` writes those rows so that **a third party can verify the copy they hold
without trusting — or even contacting — the server that produced it**.

This page is that third party's instructions.

## Getting the file

```
GET /api/audit/export                       # newline-delimited JSON, the default
GET /api/audit/export?format=json           # one document with count/truncated/filtered/rows
GET /api/audit/export?since=7d              # a window, same spellings as the task export
GET /api/audit/export?since_id=<id>         # only rows newer than a copy you already verified
```

Every row carries **both hashes** and every field the digest covers. That is deliberate: there is **no column
projection** on this export, because a verifier needs all of them and a projected export would *look* verifiable while
being unverifiable.

Response headers worth reading: `X-Audit-Export-Rows`, `X-Audit-Export-Truncated`, `X-Audit-Export-Filtered`, a
`Warning` header when the rows are a selection, and an `ETag` you can revalidate with.

## Checking it

```
go run ./cmd/verify-audit audit.ndjson          # human-readable verdict
go run ./cmd/verify-audit audit.json -json      # machine-readable
```

Exit codes: `0` every digest and link checks out, `1` a row was edited, a link was broken, **or the file holds fewer
rows than `-min-rows`**, `2` the file could not be read. The verifier walks the rows in **id order**, not file order,
because file order can be reversed.

For a pipeline:

```
go run ./cmd/verify-audit audit.ndjson -min-rows 1000 -quiet
```

`-min-rows` exists because a **truncated download** and an **emptied log** both look like "no problems found" to a naive
verifier — and both are what an attacker with write access would prefer. Set it to the number of rows you expect; `-quiet`
prints nothing and leaves the answer to the exit code.

## What this proves, and what it does not

Read this part before relying on the file.

| Detected | Not detected |
|---|---|
| one row edited in place | a rewrite that recomputes **every** row's hash |
| a row removed | an adversary who also re-links every `prev_hash` |
| a `prev_hash` rewritten | insertion of a fabricated row with a consistent hash |

The hash is **unkeyed**: the algorithm is public and `prev_hash` is **not** an input to `entry_hash`. So the chain
detects the shape an operator or a compromised account produces through the app or a database client — **partial**
edits — and not someone with write access who redoes everything. Closing that gap needs a keyed MAC or an external
anchor; this system has neither, and pretending otherwise would be worse than the limit itself.

Which is why the export matters: **an out-of-band copy you verified today cannot be matched by a table rewritten
tomorrow.** Keep the copy.

## Gaps and purges

A purge deletes rows by design, so the id sequence legitimately has gaps. Three things follow:

- `handlePurgeAuditLogs` writes an **`audit_purge` marker row** before deleting, so a gap is explainable.
- The verifier counts gaps as `discontinuities` and reports them **as a note, not a failure** — an interior deletion
  that breaks a link still fails.
- A **filtered** export (`since`, `until`, `user`, `action`, `agent_id`) is a **selection**, not a contiguous chain: the
  response says so in `X-Audit-Export-Filtered` and a `Warning` header. Verify the **unfiltered** export when the
  question is "is the chain intact"; verify a filtered one when the question is "what happened in this window".

## Incremental verification

`?since_id=<id>` returns only rows newer than the id you already hold, so a large log can be verified in pieces. The
first row of the new file links to the last row of the previous one via `prev_hash`: check that link yourself, and keep
both files — the stitching is the point.
