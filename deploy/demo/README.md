# Public read-only demo

An isolated, disposable ForgeC2 instance that anyone can look at:

- URL: `http://43.143.162.139:8000/`
- Account: `demo` / `ForgeC2Demo2026!`

It exists to show the console, not to collect anything. It runs an empty
database, one HTTP listener, and **no** beacon transport.

> **Never point this at real data.** It runs on plain HTTP with
> `CookieSecure=false`; the database, keys, and accounts are public. Do not
> reuse them, do not import a production DB, and do not leave it running as a
> general-purpose deployment. Build a normal deployment (`config.example.yaml`
> + TLS) for real work.

## What is deployed

| layer | detail |
|---|---|
| backend | container `forgec2-demo` from the published image — **canonical pin `sha256:8d996ef4…`** (the `edge` build of `f837a88cb`, the one with the patched Go standard library). The mirror currently answers 502 for that digest, so the host runs the identical image loaded from GHCR under the local tag `beacon-ops`; `verify-image.sh` proves the two are the same content. `network_mode: host`, `read_only`, `working_dir: /data`, binds `127.0.0.1:18000` only |
| database | PostgreSQL 18 on the host (`127.0.0.1:5432`, role `forgec2`, db `forgec2`) |
| edge | standalone nginx (`deploy/demo/nginx.conf`) listening on `0.0.0.0:8000`, proxying everything to the backend |
| state | `/opt/forgec2-demo/container-data` (uid/gid 65532), config mounted read-only from `/opt/forgec2-demo/compose/config.yaml` |
| secrets | `/opt/forgec2-demo/compose/.env` (mode 600); storage keys reach the process as `FORGEC2_*` env vars |

The host's own nginx is untouched: the demo gateway is a separate instance
(`nginx -c /opt/forgec2-demo/nginx.conf`).

## Read-only, at two layers

1. **Edge** — only `GET`/`HEAD` are proxied. The exceptions are `GET|POST
   /login`, `POST /api/login`, `POST /api/logout` and `POST /api/auth/extend`.
   Everything else answers `403` before the handler runs.
2. **Handler** — the `demo-observer` custom role holds only read permissions
   (`agents,listeners,tasks,files,groups,campaigns,opsec,intel,automation,
   notifications,settings,credentials,audit.read` plus `ai.use`). Every mutating
   route calls `RequirePermission(<resource>.write)` and refuses.
   `audit.read` is granted on purpose: the Audit page and its Login log are part
   of the tour, which means visitors can see sign-in records (time, user, IP,
   country) — including other visitors' and the operator's.

Not reachable by design: `/ws` and `/extc2/ws` (404 — no operator socket to
anonymous visitors). The container runs with `FORGEC2_DEMO_MODE=true`, so the
public `/health` answer carries `"demo_mode": true` and the console skips the
WebSocket connect/reconnect loop entirely, showing a **Read-only demo** badge in
the top bar instead of a permanent "reconnecting" warning.
`/payloads/*`, `/api/v1/collect|generate_204|th`, `/phishing/l/*`,
`/phishing/oauth/*`. The `/phishing` page itself is a normal read-only console
page.

The first-boot `admin` is **disabled** and its sessions are revoked at the end
of `deploy.sh`, so the public password list contains exactly one account.

## Reproduce from scratch

On a fresh Debian/Ubuntu host with Docker, nginx and PostgreSQL installed:

```bash
sudo BASE=/opt/forgec2-demo \
     PUBLIC_URL=http://<host>:8000 \
     DEMO_PASSWORD='<pick one>' \
     bash deploy/demo/deploy.sh
```

The script generates `.env` (random DB password and storage keys) and
`config.yaml` when they are missing, keeps them on re-runs, creates the
PostgreSQL role/database, installs and reloads the gateway, starts the
container, defines the role and the `demo` account through the API, verifies
`/api/me`, disables the admin, and asserts the edge behaviour (login 302,
`/ws` 404, `POST`/`PUT` 403, `/payloads` 404). It exits non-zero if any check
fails.

Requirements specific to this host and documented rather than hidden:

- **GHCR is not reachable directly.** The image is pulled through a read-only
  mirror serving the identical manifest (`ghcr.nju.edu.cn/r0si/forgec2`, pulled
  by digest `sha256:8d996ef4…`). Set `FORGEC2_IMAGE` to the mirror, tag or
  digest, in `.env`. When the mirror rejects a new digest with 502, pull it
  from GHCR on a reachable host, `docker load` the saved image, and point the
  demo at the local tag until the mirror catches up - the pin stays checkable
  either way, see `verify-image.sh` below.
- **Docker Hub is not reachable**, so PostgreSQL is the host package rather
  than the compose `postgres` profile. Swapping to the profile only means
  changing `FORGEC2_DB_DSN` and dropping the PostgreSQL section of `deploy.sh`.
- Node/Go are not needed on the host: the published image carries the embedded
  console.

## Verify

```bash
curl -s http://127.0.0.1:8000/health                      # ok
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8000/ws        # 404
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8000/extc2/ws  # 404
curl -s -o /dev/null -w '%{http_code}\n' -X POST http://127.0.0.1:8000/api/agents  # 403
curl -s -c /tmp/cj -o /dev/null -w '%{http_code}\n' \
  --data-urlencode 'username=demo' --data-urlencode 'password=ForgeC2Demo2026!' \
  http://127.0.0.1:8000/api/login                        # 302
curl -s -b /tmp/cj -H 'Accept: application/json' http://127.0.0.1:8000/loot | head -c 120
```

`/loot` returns the page shell for a browser navigation and the JSON envelope
for `Accept: application/json` — that is the dual-use contract, not a bug.

## Known limitations

- **The live channel is unavailable.** `/ws` answers 404 by design, so the
  header shows "Reconnecting…" and later "Offline", and the console logs a
  failed WebSocket handshake. Pages still poll for data.
- **Older images log PostgreSQL statements they should skip.** Builds before
  `798d00c` ran SQLite-only DDL (the `ai_knowledge_fts` FTS5 index), selected a
  removed `hash_type` column during the credential dedup backfill, and logged
  expected `record not found` lookups at ERROR. Fixed in the tree and in the
  current `edge` image; an older image shows those lines at boot but no page is
  affected.
- **AI knowledge search is trigram-indexed LIKE, not FTS5.** Under PostgreSQL
  there is no `ai_knowledge_fts` table; a pg_trgm GIN index over the blinded
  `search_tokens` column makes the LIKE predicates index scans. If the role
  cannot create the extension the index is skipped and search falls back to a
  sequential scan, which is fine at demo scale.
- **Built-in backups are disabled** for PostgreSQL (the server logs this at
  boot); use `pg_dump` as shown above.

## Operations


```bash
cd /opt/forgec2-demo/compose
docker compose ps
docker compose logs --tail=100 forgec2
docker compose restart forgec2
```

Container logs are capped by the compose `logging` options (10M × 3
files). The gateway's own access/error logs under `/opt/forgec2-demo/logs/`
are covered by `deploy/demo/logrotate.conf` (daily, 14 days, compressed) —
install it with:

```bash
sudo install -m 0644 deploy/demo/logrotate.conf /etc/logrotate.d/forgec2-demo
```

The demo needs something to show. `seed-beacon.sh` inserts one demo beacon
(offline, tenant-scoped), its task history and the audit rows for those
operations - with `prev_hash`/`entry_hash` computed in SQL from the server's
exact chain layout, so the audit page's **Verify chain** keeps reporting an
intact chain instead of a seeded row with a broken link. It is idempotent (the
beacon has a fixed id, the demo tasks are replaced, and the audit rows are
skipped when the beacon already has some) and reads the same `.env` as the
server:

```bash
sudo bash deploy/demo/seed-beacon.sh
```

The pin is checkable without the registry. `image.lock` holds the canonical
manifest's config digest and uncompressed layer digests (taken with
`crane manifest` / `crane config`), and `verify-image.sh` compares them with
the image the container actually runs - the image ID is the config digest and
the RootFS layers are the layer digests, so together they commit to the whole
filesystem of the image the digest names. A locally loaded image can
therefore be *proved* to be the canonical one:

```bash
bash deploy/demo/verify-image.sh
# OK: ghcr.nju.edu.cn/r0si/forgec2:beacon-ops is content-identical to the
# canonical manifest sha256:8d996ef4c951a423ceb7d33cd78066eed689e1f37b810d7fb812380d05b66aad
```

A backup is only real once it has been restored somewhere. `restore-drill.sh`restores the newest dump into a scratch database on the same cluster and
compares row counts on the tables that matter (needs sudo access to the
postgres superuser for database creation):

```bash
sudo bash deploy/demo/restore-drill.sh
```

Back up with `deploy/demo/backup.sh` (the built-in backup is SQLite-only andlogs a warning under PostgreSQL). It reads `DB_PASSWORD` from the same `.env`
as the server, writes a gzipped dump to `/var/backups/forgec2-demo/` and
prunes older than 7 days. Schedule it with cron or a systemd timer, e.g.
`0 2 * * * root /opt/forgec2-demo/compose/backup.sh`:

```bash
sudo bash deploy/demo/backup.sh
```

Rollback to the previous native deployment (kept from before the container
migration): stop the container, then `systemctl enable --now forgec2-demo.service`.
