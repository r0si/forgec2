# Handover bundles

An engagement handover is one zip containing everything a reviewer needs to understand what happened during a
time-boxed exercise, produced by `GET /api/engagements/{id}/bundle`.

It exists because ending an engagement means **delivering** it, and collecting the evidence by hand is four downloads
and a spreadsheet — with the audit part the step that gets skipped under pressure, which is exactly the part an
after-action reviewer needs.

## What is inside

| Member | What it is | Sensitivity |
|---|---|---|
| `summary.md` | The exercise in one page: window, fleet, outcomes, audited actions. | shareable |
| `timeline.md` | Audited actions and task outcomes interleaved by time. | shareable |
| `tasks.csv` | The tasks of the engagement hosts inside the window, in the default export columns. | contains collected output: review before sharing |
| `audit.ndjson` | The audited actions of the window, with both hashes. | verifiable offline; share in full so a gap is never mistaken for a filter |
| `agents.json` | The hosts the engagement covered. | shareable |
| `credentials.json` | Credentials found: **metadata only**, no passwords or hashes. | metadata only by design; retrieve secrets in the console where access is audited |
| `iocs.json` | Indicators extracted from the engagement hosts. | bound is a day count spanning the engagement, not the exact window |
| `plugins.json` | The plugin set the work depended on, with package digests. | shareable |
| `MANIFEST.json` | The package describing itself: purpose, source, sensitivity and SHA-256 for every member. | shareable |

The same table is inside `MANIFEST.json`, generated from the members actually written, so a receiver who only has the
archive does not need this file.

**Credentials never travel with secrets.** A bundle leaves the console — it gets emailed, put on a share, attached to a
report — so passwords and hashes stay in the system that audits their use.

## Choosing what to send

`?include=` takes a comma-separated list of members:

```
GET /api/engagements/7/bundle?include=summary.md,timeline.md,audit.ndjson
```

`MANIFEST.json` is always written and is **not** selectable: it is generated from whatever was written, so a package
without it could not be checked by anyone. An unknown member name is refused with the list of valid ones rather than
silently ignored.

## Checking a bundle

Three questions, and both paths answer all three:

1. **Did the files survive transit?** Every member's SHA-256 is in the manifest, captured as the bytes entered the
   archive.
2. **Does the manifest still describe them?** Recomputing the digests catches a changed, missing or unlisted member.
3. **Was it signed by the key we already trust?** See below — this is the one that is easy to get wrong.

### Without a server

```
go run ./cmd/verify-bundle handover.zip                 # integrity only
go run ./cmd/verify-bundle handover.zip -key <hex>      # integrity + authenticity
go run ./cmd/verify-bundle handover.zip -json           # machine-readable
```

Exit codes: `0` the archive matches its manifest, `1` a member is damaged, missing or unlisted, `2` the file could not
be read as a bundle at all, `3` the archive is intact but **not trusted** (`-require-trusted` only).

For a pipeline that must only accept provable handovers:

```
go run ./cmd/verify-bundle handover.zip -key <hex> -require-trusted -quiet
```

`-quiet` prints nothing and leaves the decision to the exit code, and `3` is separate from `1` on purpose: a broken file
is a transfer problem worth retrying, an unproven one is a trust decision worth escalating.

### With a server

```
POST /api/engagements/{id}/bundle/verify?public_key=<hex>
Content-Type: application/zip
<body: the zip>
```

The response reports `ok`, `checked`, `verified`, `mismatched`, `unverifiable`, `extra` and a `signature` block.

Both paths accept the archive's own `X-Bundle-SHA256` response header, which is the digest of the **zip file** — the
check to run before opening anything.

## valid is not trusted

This is the distinction worth reading twice.

A signature is made with `FORGEC2_BUNDLE_SIGNING_KEY` (a hex 32-byte Ed25519 seed) and published in the manifest
together with its public key. Because **the key travels inside the archive**, a package rebuilt wholesale — manifest,
digests and key all regenerated — verifies against itself. That is not a defect of the implementation; it is what an
unkeyed, self-contained package can prove.

So `-key` / `?public_key=` takes the key **you already trust**, obtained out of band:

| Situation | `valid` | `trusted` |
|---|---|---|
| Signed, no key supplied | yes | **no** — the key came from the package |
| Signed, matching key supplied | yes | yes |
| Signed, different key supplied | yes | **no** — reported as a different key |
| Unsigned | no | no — reported as unsigned |
| Signed, then a digest edited | **no** | no |

Without a configured signing key, bundles are produced **unsigned**, the startup log says so, and
`GET /api/exports/capabilities` reports it. An unsigned bundle still detects damage; it simply cannot prove origin.

## Limits

- A bundle larger than the cap is **refused with 413 rather than truncated**: a half-delivered bundle is a handover
  that silently lacks evidence. Export the parts separately and send them next to it.
- `iocs.json` is bounded by a **day count spanning the engagement**, not by the exact window the other members use. That
  is stated in the file itself.
- The bundle is tenant-scoped: asking for an engagement in another tenant answers 404.
- Producing and verifying a bundle are both audited (`engagement_bundle`, `engagement_bundle_verify`).
