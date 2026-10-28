# Closing out an engagement

The last hour of an exercise has four steps, and each one exists because skipping it has a cost: a host that keeps
answering after the engagement ends, a package nobody can verify, or a credential inventory sent to a customer.

## 1. Stop accepting work

The deadline is enforced at dispatch, so closing an engagement already refuses new tasks for its hosts. Two extra steps
make that visible where people look:

```sh
# End the exercise (bookkeeping). Add stamp_agents to also write the deadline onto every member host.
curl -X POST $C2/api/engagements/7/close \
     -H 'Content-Type: application/json' -d '{"stamp_agents": true}'

# Or set one deadline across a selection, without closing anything:
curl -X POST $C2/api/agents/bulk/expiry \
     -H 'Content-Type: application/json' \
     -d '{"engagement_id": 7, "expires_at": "2026-11-01T18:00:00Z"}'
```

`stamp_agents` is **opt-in**: a close is a bookkeeping decision and must not silently rewrite forty agent rows. The bulk
form reports `skipped` for ids the caller cannot see — *"I set 39 of 40"* must not look like success.

## 2. Assemble the handover

```sh
curl -L -o handover.zip "$C2/api/engagements/7/bundle"

# For a customer who must not receive the credential inventory:
curl -L -o handover.zip "$C2/api/engagements/7/bundle?include=summary.md,timeline.md,tasks.csv,audit.ndjson"
```

Every archive carries `MANIFEST.json` — what each member is, its sensitivity, a SHA-256 of the bytes, and the release that
produced it. `credentials.json` is **metadata only**: a bundle leaves the console, so passwords and hashes stay behind, in
the system that audits their use.

## 3. Verify before sending

```sh
go run ./cmd/verify-bundle handover.zip -key <hex public key> -require-complete
```

Exit codes: `0` intact and provable, `1` damaged, `2` unreadable, `3` intact but **not signed by that key**, `4` intact
but **incomplete**. The archive's `X-Bundle-SHA256` response header is the digest of the zip itself — compare it before
opening anything, which is the check that matters once the file has been emailed around.

## 4. Hand over, and be able to say what you handed over

Both the assembly and any verification are audited (`engagement_bundle`, `engagement_bundle_verify`), and the close is
audited separately from the stamp (`engagement_close`, `engagement_close_stamp`). When someone asks *"when did this
exercise end and what was delivered"*, the audit log answers with timestamps rather than recollection.

The receiver's instructions are in `docs/HANDOVER_BUNDLE.md`; the verification limits — what the digests and signatures
can and cannot prove — are in `docs/VERIFIERS.md`.
