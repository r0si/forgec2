#!/bin/bash
# Seed the public demo with one beacon, its task history and the matching
# audit rows.
#
#   sudo bash deploy/demo/seed-beacon.sh
#
# Why a script and not the API: the demo deployment is read-only for
# operators (FORGEC2_DEMO_MODE=true), so POST /api/agents answers 403. The
# rows are inserted directly instead.
#
# The audit rows are appended with the same hash chain the server writes:
# prev_hash/entry_hash are computed in SQL from the server's exact field
# layout ("user|action|resource|agent_id|ip|success|error|details", sha256),
# so the audit page's "Verify chain" button keeps reporting an intact chain
# instead of a seeded row with a broken link.
#
# Idempotent: the beacon is inserted with a fixed id (ON CONFLICT DO NOTHING),
# the demo tasks are replaced, and the audit rows are skipped when the beacon
# already has some.
#
# Requires the same .env the server uses (DB_PASSWORD); pass BACKUP_ENV to
# override.
set -euo pipefail

ENV_FILE="${BACKUP_ENV:-/opt/forgec2-demo/compose/.env}"
[ -r "$ENV_FILE" ] || { echo "env file not readable: $ENV_FILE" >&2; exit 1; }
# shellcheck disable=SC1090
. "$ENV_FILE"
: "${DB_PASSWORD:?DB_PASSWORD missing in $ENV_FILE}"
export PGPASSWORD="$DB_PASSWORD"

psql -v ON_ERROR_STOP=1 -h 127.0.0.1 -U forgec2 -d forgec2 <<'SQL'
\set beacon_id 'a3f1c2d4-5e6b-4c7d-8e9f-0a1b2c3d4e5f'

-- The beacon itself. Offline on purpose: nothing is really checking in, and
-- an "online" demo beacon would be a lie that invites operators to wait for
-- tasks that will never run.
INSERT INTO implants (
    id, tenant_id, hostname, username, os, arch, ip, public_ip,
    country, city, latitude, longitude, last_seen, status,
    trusted, blocked, notes, tags, version, protocol_version, pid,
    process_name, integrity, domain, current_interval, current_jitter,
    tls_profile, registered, created_at, updated_at
) VALUES (
    :'beacon_id',
    COALESCE((SELECT tenant_id FROM users WHERE username = 'demo' LIMIT 1), 1),
    'demo-workstation-01', 'svc_backup', 'windows', 'amd64',
    '10.20.30.40', '198.51.100.77',
    '美国', '西雅图', 47.6062, -122.3321,
    now() - interval '18 minutes', 'offline',
    true, false,
    'Demo beacon for the public demo (read-only).',
    'demo,windows',
    '2.7.1', 1, 4242, 'forgec2.exe', 'High', 'CORP',
    60, 10, 'chrome_auto', true,
    now() - interval '3 days', now()
) ON CONFLICT (id) DO NOTHING;

-- Its task history, so the beacon pages have something to show. Replaced on
-- every run to keep the demo deterministic.
DELETE FROM tasks WHERE agent_id = :'beacon_id';
INSERT INTO tasks (tenant_id, agent_id, type, command, status, result, error, created_by, created_at, updated_at) VALUES
    (COALESCE((SELECT tenant_id FROM users WHERE username = 'demo' LIMIT 1), 1), :'beacon_id',
     'shell', 'whoami', 'completed', 'corp\svc_backup', '', 'demo',
     now() - interval '2 hours', now() - interval '2 hours'),
    (COALESCE((SELECT tenant_id FROM users WHERE username = 'demo' LIMIT 1), 1), :'beacon_id',
     'screenshot', '', 'completed', 'capture saved', '', 'demo',
     now() - interval '70 minutes', now() - interval '70 minutes'),
    (COALESCE((SELECT tenant_id FROM users WHERE username = 'demo' LIMIT 1), 1), :'beacon_id',
     'shell', 'whoami /priv', 'failed', '', 'exit status 1: access denied', 'demo',
     now() - interval '25 minutes', now() - interval '25 minutes');

-- The audit rows for those operations, chained onto the current head of the
-- audit hash chain. Skipped when the beacon already has audit rows, so the
-- chain is never forked.
DO $$
DECLARE
    v_agent  text := 'a3f1c2d4-5e6b-4c7d-8e9f-0a1b2c3d4e5f';
    v_tenant bigint;
    v_prev   text;
    v_hash   text;
BEGIN
    IF EXISTS (SELECT 1 FROM audit_logs WHERE agent_id = v_agent) THEN
        RAISE NOTICE 'audit rows for the demo beacon already exist; skipping';
        RETURN;
    END IF;
    SELECT tenant_id INTO v_tenant FROM users WHERE username = 'demo' LIMIT 1;
    IF v_tenant IS NULL THEN
        v_tenant := 1;
    END IF;
    SELECT entry_hash INTO v_prev FROM audit_logs ORDER BY id DESC LIMIT 1;

    v_hash := encode(sha256(convert_to(
        'demo|operator:task_create|task|' || v_agent || '|10.0.0.5|true||shell task queued: whoami',
        'UTF8')), 'hex');
    INSERT INTO audit_logs ("user", action, resource, agent_id, tenant_id, ip, success, error, details, prev_hash, entry_hash, created_at)
    VALUES ('demo', 'operator:task_create', 'task', v_agent, v_tenant, '10.0.0.5', true, '',
            'shell task queued: whoami', v_prev, v_hash, now() - interval '2 hours');
    v_prev := v_hash;

    v_hash := encode(sha256(convert_to(
        'demo|operator:task_create|task|' || v_agent || '|10.0.0.5|true||screenshot task queued',
        'UTF8')), 'hex');
    INSERT INTO audit_logs ("user", action, resource, agent_id, tenant_id, ip, success, error, details, prev_hash, entry_hash, created_at)
    VALUES ('demo', 'operator:task_create', 'task', v_agent, v_tenant, '10.0.0.5', true, '',
            'screenshot task queued', v_prev, v_hash, now() - interval '70 minutes');
    v_prev := v_hash;

    v_hash := encode(sha256(convert_to(
        'demo|command_result|task|' || v_agent || '|10.0.0.5|false|exit status 1: access denied|whoami /priv failed',
        'UTF8')), 'hex');
    INSERT INTO audit_logs ("user", action, resource, agent_id, tenant_id, ip, success, error, details, prev_hash, entry_hash, created_at)
    VALUES ('demo', 'command_result', 'task', v_agent, v_tenant, '10.0.0.5', false,
            'exit status 1: access denied', 'whoami /priv failed', v_prev, v_hash, now() - interval '25 minutes');
END $$;
SQL

echo "demo beacon seeded: a3f1c2d4-5e6b-4c7d-8e9f-0a1b2c3d4e5f (demo-workstation-01)"
