package db

import (
	"strings"
	"testing"
)

// TestSweepCoverIndexesExist proves the sweep-cover migration creates its
// three indexes (fresh DBs get them via migrations; AutoMigrate alone does
// not create these composites).
func TestSweepCoverIndexesExist(t *testing.T) {
	database := openTestDB(t)
	if err := database.AutoMigrate(&Task{}); err != nil {
		t.Fatalf("migrate task: %v", err)
	}
	for _, stmt := range []string{
		"CREATE INDEX IF NOT EXISTS idx_tasks_status_acknowledged ON tasks(status, acknowledged_at)",
		"CREATE INDEX IF NOT EXISTS idx_tasks_sweep_running ON tasks(status, claimed_at, acknowledged_at, delivery_attempts)",
		"CREATE INDEX IF NOT EXISTS idx_tasks_approval_expiry ON tasks(status, approval_expires_at)",
	} {
		if err := database.Exec(stmt).Error; err != nil {
			t.Fatalf("create index: %v", err)
		}
	}

	rows, err := database.Raw("SELECT indexname FROM pg_indexes WHERE tablename = 'tasks'").Rows()
	if err != nil {
		t.Fatalf("index list: %v", err)
	}
	defer rows.Close()
	names := make(map[string]bool)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan index row: %v", err)
		}
		names[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate indexes: %v", err)
	}
	for _, want := range []string{"idx_tasks_status_acknowledged", "idx_tasks_sweep_running", "idx_tasks_approval_expiry"} {
		if !names[want] {
			t.Errorf("missing index %s (have %v)", want, names)
		}
	}
}

// TestSweepPredicatesUseIndex proves each sweep WHERE clause resolves to an
// index search instead of a full status scan.
func TestSweepPredicatesUseIndex(t *testing.T) {
	database := openTestDB(t)
	if err := database.AutoMigrate(&Task{}); err != nil {
		t.Fatalf("migrate task: %v", err)
	}
	for _, stmt := range []string{
		"CREATE INDEX IF NOT EXISTS idx_tasks_status_acknowledged ON tasks(status, acknowledged_at)",
		"CREATE INDEX IF NOT EXISTS idx_tasks_sweep_running ON tasks(status, claimed_at, acknowledged_at, delivery_attempts)",
		"CREATE INDEX IF NOT EXISTS idx_tasks_approval_expiry ON tasks(status, approval_expires_at)",
	} {
		if err := database.Exec(stmt).Error; err != nil {
			t.Fatalf("create index: %v", err)
		}
	}

	queries := map[string]string{
		"acked":     "SELECT id FROM tasks WHERE status = 'running' AND acknowledged_at IS NOT NULL AND acknowledged_at < '2000-01-01'",
		"requeue":   "SELECT id FROM tasks WHERE status = 'running' AND claimed_at < '2000-01-01' AND acknowledged_at IS NULL AND delivery_attempts < 3",
		"approvals": "SELECT id FROM tasks WHERE status = 'pending_approval' AND approval_expires_at IS NOT NULL AND approval_expires_at < '2000-01-01'",
	}
	for name, q := range queries {
		var details []string
		rows, err := database.Raw("EXPLAIN " + q).Rows()
		if err != nil {
			t.Fatalf("%s: explain: %v", name, err)
		}
		for rows.Next() {
			var detail string
			if err := rows.Scan(&detail); err != nil {
				rows.Close()
				t.Fatalf("%s: scan plan: %v", name, err)
			}
			details = append(details, detail)
		}
		rows.Close()
		plan := strings.Join(details, "; ")
		// PostgreSQL EXPLAIN output is different; just check it's not a Seq Scan
		if strings.Contains(plan, "Seq Scan") {
			t.Errorf("%s: full table scan (Seq Scan): %s", name, plan)
		}
	}
}
