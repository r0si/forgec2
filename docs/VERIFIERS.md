# Offline verifiers

Three artefacts leave a ForgeC2 deployment and get checked somewhere else: a **handover bundle**, an **audit export**,
and a **plugin package**. All three carry what a receiver needs to check them without a running console, and each has a
command for it:

| Artefact | Command | Guide |
|---|---|---|
| Engagement handover (`.zip`) | `cmd/verify-bundle` | [HANDOVER_BUNDLE.md](HANDOVER_BUNDLE.md) |
| Audit export (`.ndjson` / `.json`) | `cmd/verify-audit` | [AUDIT_EXPORT.md](AUDIT_EXPORT.md) |
| Plugin package (`.zip` / directory) | `cmd/verify-plugin` | [PLUGIN_AUTHORING.md](PLUGIN_AUTHORING.md) |

## The same interface on purpose

```
go run ./cmd/verify-bundle handover.zip -key <hex> [-require-trusted] [-quiet] [-json]
go run ./cmd/verify-audit  audit.ndjson [-min-rows N]       [-quiet] [-json]
go run ./cmd/verify-plugin package.zip -key <hex>           [-quiet] [-json]
```

Exit codes:

| Code | Meaning |
|---|---|
| `0` | The artefact matches what it claims, and where a key was required, it matched |
| `1` | The artefact is wrong: damaged, edited, missing a member, or signed by a different key |
| `2` | The file could not be read as this kind of artefact at all (not a zip, not JSON, no manifest) |
| `3` | `verify-bundle -require-trusted` only: **intact but not provably the sender's** |
| `4` | `verify-bundle -require-complete` only: **intact, but not the whole handover** |

The switches that raise these codes exist because the artefacts are legitimately allowed to be partial:

| Switch | The question it asks |
| --- | --- |
| `verify-bundle -require-trusted` | Is this signed by the key **I** hold? (`?include=` and signing are independent) |
| `verify-bundle -require-complete` | Is this the **whole** handover, or a selection someone made with `?include=`? |
| `verify-audit -min-rows N` | Did the **transfer** complete, or did the file stop early? |
| `verify-audit -require-contiguous` | Is this the **whole chain**, or a filtered/truncated selection? An NDJSON file cannot prove it |

**Four different failures, four different codes.** A broken package needs a re-send; an unproven one needs a trust
decision; an incomplete one needs a conversation; a short file needs another download. A pipeline that collapses them
does the wrong thing with at least one.

**Why 3 is separate from 1.** A damaged file is a **transfer problem** — retry it. An intact file whose signature cannot
be tied to a key you trust is a **trust decision** — escalate it. A pipeline that cannot tell them apart does the wrong
thing with one of them.

`-quiet` prints nothing and leaves the answer to the exit code. `-json` prints a machine-readable report.

## What none of them can prove

Stated once, here, because all three share it:

- **The bundle and audit digests are unkeyed.** They detect damage and **partial** edits — a changed file, a removed row,
  a rewritten link — and not an adversary who recomputes everything. The audit chain additionally does not hash
  `prev_hash`, so a full rewrite is invisible to it.
- **A signature proves origin only against a key you already hold.** `verify-bundle` recomputes `trusted` from `-key`
  for exactly this reason: a key carried inside the package proves consistency, nothing more.
- **`verify-audit -min-rows` exists because an empty or truncated file otherwise verifies.** Set it to the number of rows
  you expect.

None of this is a defect of the tools; it is what the underlying primitives can and cannot do. A verifier that implied
more would be worse than no verifier.
