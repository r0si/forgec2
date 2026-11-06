package db

import (
	"strings"
	"testing"

	"gorm.io/gorm"
)

// hotPathIndexID is the migration that covers the read shapes the earlier
// migrations missed.
const hotPathIndexID = "2026-10-15-hot-path-read-indexes"

// implantListenerIndexID is the migration that adds the composite index the
// listener detail page's ordered agent page and its active counters need.
// Later index batches get their own migration ID for the same reason: an
// already-applied migration is never edited retroactively, because databases
// that ran it would never see the added index.
const implantListenerIndexID = "2026-10-16-implant-listener-read-index"

// hotPathProbe is one (index, query) pair taken verbatim from a handler. The
// query is the shape the planner actually sees, including the tenant predicate
// where the handler has one — measuring a simplified query is how a "useful"
// index gets shipped that the real path never touches.
//
// pre lists the indexes that already exist on the table in production (from
// earlier migrations). AutoMigrate only creates the gorm:"index" tag ones, so
// without this the baseline is missing indexes the planner would really weigh
// and the measured before/after can be wrong in both directions.
type hotPathProbe struct {
	index   string
	table   string
	before  string // plan fragment proving the un-indexed shape was bad
	query   string
	creates []interface{}
	pre     []string
	// migration is the index migration that creates the index. Empty means
	// hotPathIndexID.
	migration string
}

// migrationID is the migration a probe's index belongs to.
func (p hotPathProbe) migrationID() string {
	if p.migration != "" {
		return p.migration
	}
	return hotPathIndexID
}

// taskBaseline / friends are the pre-existing index sets per table, kept here
// so the measured "before" plan matches a production database. IF NOT EXISTS
// throughout: AutoMigrate already creates the gorm:"index" tag ones.
var (
	taskBaseline = []string{
		"CREATE INDEX IF NOT EXISTS idx_tasks_status ON tasks(status)",
		"CREATE INDEX IF NOT EXISTS idx_tasks_created_status ON tasks(created_at, status)",
		"CREATE INDEX IF NOT EXISTS idx_tasks_status_created ON tasks(status, created_at)",
		"CREATE INDEX IF NOT EXISTS idx_tasks_type ON tasks(type)",
	}
	alertBaseline = []string{
		"CREATE INDEX IF NOT EXISTS idx_alerts_status ON alerts(status)",
		"CREATE INDEX IF NOT EXISTS idx_alerts_severity ON alerts(severity)",
		"CREATE INDEX IF NOT EXISTS idx_alerts_tenant_id ON alerts(tenant_id)",
	}
	notificationBaseline = []string{
		"CREATE INDEX IF NOT EXISTS idx_notifications_read_created ON notifications(read, created_at)",
		"CREATE INDEX IF NOT EXISTS idx_notifications_type_read ON notifications(type, read)",
		"CREATE INDEX IF NOT EXISTS idx_notifications_type_read_created ON notifications(type, read, created_at)",
	}
	tokenBaseline = []string{
		"CREATE INDEX IF NOT EXISTS idx_token_entries_active ON token_entries(active)",
		"CREATE INDEX IF NOT EXISTS idx_token_entries_domain ON token_entries(domain)",
		"CREATE INDEX IF NOT EXISTS idx_token_entries_agent_active ON token_entries(agent_id, active)",
	}
	buildLogBaseline = []string{
		"CREATE INDEX IF NOT EXISTS idx_build_logs_user ON build_logs(\"user\")",
		"CREATE INDEX IF NOT EXISTS idx_build_logs_status ON build_logs(status)",
		"CREATE INDEX IF NOT EXISTS idx_build_logs_listener_id ON build_logs(listener_id)",
	}
	// ChatMessage.channel carries no gorm:"index" tag, so the earlier migration
	// is the only thing that indexes it. (Its sibling migration also tries to
	// index a session_id column the model does not have.)
	chatBaseline = []string{
		"CREATE INDEX IF NOT EXISTS idx_chat_messages_channel ON chat_messages(channel)",
	}
	// Implant carries gorm:"index" tags for last_seen and status, and the
	// initial-schema index migration adds the single-column listener_id index.
	// The composite probe below starts from exactly those, so its measured
	// "before" is the plan a production database has today.
	implantBaseline = []string{
		"CREATE INDEX IF NOT EXISTS idx_implants_listener_id ON implants(listener_id)",
		"CREATE INDEX IF NOT EXISTS idx_implants_last_seen ON implants(last_seen)",
		"CREATE INDEX IF NOT EXISTS idx_implants_status ON implants(status)",
		"CREATE INDEX IF NOT EXISTS idx_implants_status_last_seen ON implants(status, last_seen)",
		"CREATE INDEX IF NOT EXISTS idx_implants_parent_agent ON implants(parent_agent_id)",
	}
)

func hotPathProbes() []hotPathProbe {
	return []hotPathProbe{
		{
			index: "idx_tasks_status_updated", table: "tasks",
			creates: []interface{}{&Task{}}, pre: taskBaseline,
			// metric_alerts.go:151 — failure rate, evaluated every 30s.
			query:  "SELECT COUNT(*) FROM tasks WHERE status IN ('completed','failed') AND updated_at > '2020-01-01'",
			before: "idx_tasks_status",
		},
		{
			index: "idx_tasks_created_by_created", table: "tasks",
			creates: []interface{}{&Task{}}, pre: taskBaseline,
			// ai_tools_exec.go:805-810 — creator filter + created_at DESC.
			query:  "SELECT * FROM tasks WHERE created_by = 'ai' ORDER BY created_at DESC LIMIT 20",
			before: "idx_tasks_created_at",
		},
		{
			index: "idx_alerts_tenant_created", table: "alerts",
			creates: []interface{}{&Alert{}}, pre: alertBaseline,
			// handlers_monitor.go:473-483 — tenant-scoped, ordered by recency.
			query:  "SELECT * FROM alerts WHERE tenant_id = 1 ORDER BY created_at DESC LIMIT 100",
			before: "USE TEMP B-TREE FOR ORDER BY",
		},
		{
			index: "idx_notifications_severity_created", table: "notifications",
			creates: []interface{}{&Notification{}}, pre: notificationBaseline,
			// handlers_notifications.go:23,40 — severity filter + created_at DESC.
			query:  "SELECT * FROM notifications WHERE severity = 'high' ORDER BY created_at DESC LIMIT 20",
			before: "USE TEMP B-TREE FOR ORDER BY",
		},
		{
			index: "idx_token_entries_agent_created", table: "token_entries",
			creates: []interface{}{&TokenEntry{}}, pre: tokenBaseline,
			// handlers_token.go:36,289 — per-agent tokens, newest first.
			query:  "SELECT * FROM token_entries WHERE agent_id = 'a1' ORDER BY created_at DESC",
			before: "USE TEMP B-TREE FOR ORDER BY",
		},
		{
			index: "idx_build_logs_platform_created", table: "build_logs",
			creates: []interface{}{&BuildLog{}}, pre: buildLogBaseline,
			// handlers_ops.go:80,93 — platform filter + created_at DESC.
			query:  "SELECT * FROM build_logs WHERE platform = 'windows' ORDER BY created_at DESC LIMIT 20",
			before: "USE TEMP B-TREE FOR ORDER BY",
		},
		{
			index: "idx_build_logs_status_created", table: "build_logs",
			creates: []interface{}{&BuildLog{}}, pre: buildLogBaseline,
			// handlers_build_effectiveness.go:42 — status plus a time window.
			query:  "SELECT * FROM build_logs WHERE status = 'success' AND created_at >= '2020-01-01' ORDER BY created_at DESC LIMIT 500",
			before: "USE TEMP B-TREE FOR ORDER BY",
		},
		{
			index: "idx_chat_messages_channel_created", table: "chat_messages",
			creates: []interface{}{&ChatMessage{}}, pre: chatBaseline,
			// handlers_missing_api.go:264 — channel history, oldest first.
			query:  "SELECT * FROM chat_messages WHERE channel = 'general' ORDER BY created_at ASC LIMIT 200",
			before: "USE TEMP B-TREE FOR ORDER BY",
		},
		{
			index: "idx_user_sessions_user_created", table: "user_sessions",
			creates: []interface{}{&UserSession{}}, pre: nil,
			// handlers_sessions.go:130 — a user's live sessions, newest first.
			query:  "SELECT * FROM user_sessions WHERE user_id = 1 AND revoked_at = '0001-01-01 00:00:00' AND expires_at > '2020-01-01' ORDER BY created_at DESC",
			before: "USE TEMP B-TREE FOR ORDER BY",
		},
		{
			index: "idx_api_keys_user_created", table: "api_keys",
			creates: []interface{}{&APIKey{}}, pre: nil,
			// handlers_apikeys.go:163 — a user's API keys, newest first.
			query:  "SELECT * FROM api_keys WHERE user_id = 1 ORDER BY created_at DESC",
			before: "USE TEMP B-TREE FOR ORDER BY",
		},
		{
			index: "idx_implants_listener_seen", table: "implants",
			creates: []interface{}{&Implant{}}, pre: implantBaseline,
			migration: implantListenerIndexID,
			// handlers_listeners.go:77,114 — the listener detail page: the
			// listener's agents, newest first, before the 5000-row page cap.
			query:  "SELECT * FROM implants WHERE listener_id = 1 ORDER BY last_seen DESC LIMIT 5000",
			before: "USE TEMP B-TREE FOR ORDER BY",
		},
		{
			index: "idx_implants_listener_seen", table: "implants",
			creates: []interface{}{&Implant{}}, pre: implantBaseline,
			migration: implantListenerIndexID,
			// handlers_listeners.go:82,121 — active agents on the listener:
			// the same predicate plus a last_seen window, served as an ordered
			// range instead of a seek plus per-row lookups.
			query:  "SELECT COUNT(*) FROM implants WHERE listener_id = 1 AND last_seen > '2020-01-01'",
			before: "USING INDEX idx_implants_listener_id",
		},
	}
}

func hotPathDB(t *testing.T, p hotPathProbe) *gorm.DB {
	t.Helper()
	database := openTestDB(t)
	if err := database.AutoMigrate(p.creates...); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for _, stmt := range p.pre {
		if err := database.Exec(stmt).Error; err != nil {
			t.Fatalf("baseline index %q: %v", stmt, err)
		}
	}
	return database
}

func hotPathPlan(t *testing.T, database *gorm.DB, q string) string {
	t.Helper()
	// PostgreSQL uses EXPLAIN instead of EXPLAIN QUERY PLAN
	rows, err := database.Raw("EXPLAIN " + q).Rows()
	if err != nil {
		t.Fatalf("explain %q: %v", q, err)
	}
	defer rows.Close()
	var parts []string
	for rows.Next() {
		var detail string
		if err := rows.Scan(&detail); err != nil {
			t.Fatalf("scan plan: %v", err)
		}
		parts = append(parts, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate plan: %v", err)
	}
	return strings.Join(parts, "; ")
}

func hasIndex(t *testing.T, database *gorm.DB, table, want string) bool {
	t.Helper()
	// PostgreSQL: use pg_indexes instead of PRAGMA index_list
	rows, err := database.Raw("SELECT indexname FROM pg_indexes WHERE tablename = ?", table).Rows()
	if err != nil {
		t.Fatalf("pg_indexes(%s): %v", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan index row: %v", err)
		}
		if name == want {
			return true
		}
	}
	return false
}

// TestHotPathIndexesCreatedByMigration proves the migrations really create
// every index the probes name. A typo in one SQL string would otherwise only
// surface as a silently slow query in production.
func TestHotPathIndexesCreatedByMigration(t *testing.T) {
	for _, p := range hotPathProbes() {
		database := hotPathDB(t, p)
		if err := findMigration(t, p.migrationID()).Migrate(database); err != nil {
			t.Fatalf("migrate: %v", err)
		}
		if !hasIndex(t, database, p.table, p.index) {
			t.Errorf("migration did not create %s on %s", p.index, p.table)
		}
	}
}

// TestHotPathIndexChangesTheQueryPlan is the load-bearing assertion: each index
// must actually be chosen for its query. An index nobody uses is pure write
// overhead, so a plan that still scans or still sorts is a failure, not a pass.
// The "before" assertion matters just as much: if a future schema change makes
// an already-good query shape, the baseline check fails and the index gets
// re-examined instead of silently accumulating.
func TestHotPathIndexChangesTheQueryPlan(t *testing.T) {
	for _, p := range hotPathProbes() {
		t.Run(p.index, func(t *testing.T) {
			database := hotPathDB(t, p)

			before := hotPathPlan(t, database, p.query)
			if !strings.Contains(before, p.before) {
				t.Fatalf("setup changed: expected the pre-index plan to use %q, got %s", p.before, before)
			}

			if err := findMigration(t, p.migrationID()).Migrate(database); err != nil {
				t.Fatalf("migrate: %v", err)
			}
			after := hotPathPlan(t, database, p.query)

			if !strings.Contains(after, p.index) {
				t.Fatalf("plan ignores %s: %s", p.index, after)
			}
			if strings.Contains(after, "USE TEMP B-TREE FOR ORDER BY") {
				t.Fatalf("plan still sorts after indexing %s: %s", p.index, after)
			}
			if strings.Contains(after, "SCAN "+p.table) && !strings.Contains(after, "USING") {
				t.Fatalf("plan still fully scans %s: %s", p.table, after)
			}
		})
	}
}

// TestHotPathIndexRollbackDropsEveryIndex keeps the rollback honest: an index
// the migration cannot remove is an index a down-migration leaves behind.
func TestHotPathIndexRollbackDropsEveryIndex(t *testing.T) {
	for _, p := range hotPathProbes() {
		database := hotPathDB(t, p)
		mig := findMigration(t, p.migrationID())
		if err := mig.Migrate(database); err != nil {
			t.Fatalf("migrate: %v", err)
		}
		if err := mig.Rollback(database); err != nil {
			t.Fatalf("rollback: %v", err)
		}
		if hasIndex(t, database, p.table, p.index) {
			t.Errorf("rollback left %s on %s", p.index, p.table)
		}
	}
}

// TestHotPathIndexesAreIdempotent proves re-running the migrations on an
// already-indexed database is a no-op, which is what the migration runner does
// on every boot.
func TestHotPathIndexesAreIdempotent(t *testing.T) {
	probes := hotPathProbes()
	database := hotPathDB(t, probes[0])
	for i := 1; i < len(probes); i++ {
		if err := database.AutoMigrate(probes[i].creates...); err != nil {
			t.Fatalf("migrate %s: %v", probes[i].table, err)
		}
		for _, stmt := range probes[i].pre {
			if err := database.Exec(stmt).Error; err != nil {
				t.Fatalf("baseline %q: %v", stmt, err)
			}
		}
	}
	for i := 0; i < 3; i++ {
		for _, id := range probeMigrationIDs(probes) {
			if err := findMigration(t, id).Migrate(database); err != nil {
				t.Fatalf("migrate %s run %d: %v", id, i+1, err)
			}
		}
	}
	for _, p := range probes {
		if !hasIndex(t, database, p.table, p.index) {
			t.Errorf("repeated migration lost %s", p.index)
		}
	}
}

// probeMigrationIDs lists the distinct migrations the probes depend on, in
// probe order.
func probeMigrationIDs(probes []hotPathProbe) []string {
	var ids []string
	seen := map[string]bool{}
	for _, p := range probes {
		if id := p.migrationID(); !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	return ids
}

// TestHotPathIndexesSkippedWhenTableMissing proves the migration tolerates a
// partially migrated database instead of aborting startup.
func TestHotPathIndexesSkippedWhenTableMissing(t *testing.T) {
	database := hotPathDB(t, hotPathProbe{creates: []interface{}{&Task{}}})
	if err := findMigration(t, hotPathIndexID).Migrate(database); err != nil {
		t.Fatalf("migration must not fail on a missing table: %v", err)
	}
	if !hasIndex(t, database, "tasks", "idx_tasks_status_updated") {
		t.Error("the tasks index should still be created when other tables are absent")
	}
}
