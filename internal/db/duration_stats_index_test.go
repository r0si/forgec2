package db

import (
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// TestDurationStatsQueryUsesAnIndex covers the query behind GET /api/stats/tasks/duration.
//
// It is a per-request read that filters a tenant's final-state tasks by time and orders them newest
// first, so the shape an operator uses most ("how slow was today") must be served by an index instead
// of scanning the tenant's whole task history and sorting it in a TEMP B-TREE. Measured before claiming:
// the plans below are printed, and the assertions are on the specific access path.
func TestDurationStatsQueryUsesAnIndex(t *testing.T) {
	database := setupTestDB(t)
	if err := database.AutoMigrate(&Task{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	queries := map[string]string{
		// The endpoint's own projection and filters (group_by=type, no type filter).
		"duration_stats_tenant_window": `SELECT type, agent_id, status, created_at, updated_at FROM tasks
			WHERE tenant_id = 1 AND status IN ('completed','failed')
			AND created_at >= '2026-10-01 00:00:00' AND updated_at > created_at
			ORDER BY created_at DESC LIMIT 5000`,
		// The filtered variant (?type=shell).
		"duration_stats_type_window": `SELECT type, agent_id, status, created_at, updated_at FROM tasks
			WHERE tenant_id = 1 AND type = 'shell' AND status IN ('completed','failed')
			AND created_at >= '2026-10-01 00:00:00' AND updated_at > created_at
			ORDER BY created_at DESC LIMIT 5000`,
		// The bulk-rerun candidate query: same tenant+agent+status+window shape.
		"bulk_rerun_candidates": `SELECT * FROM tasks
			WHERE tenant_id = 1 AND agent_id = 'a-1' AND status = 'failed'
			AND created_at >= '2026-10-01 00:00:00'
			ORDER BY created_at DESC LIMIT 11`,
	}

	for name, query := range queries {
		var plan []struct {
			Detail string `gorm:"column:detail"`
		}
		if err := database.Raw("EXPLAIN QUERY PLAN " + query).Scan(&plan).Error; err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var details []string
		for _, row := range plan {
			details = append(details, row.Detail)
		}
		joined := strings.Join(details, " | ")
		t.Logf("%s: %s", name, joined)

		if strings.Contains(joined, "SCAN tasks") {
			t.Errorf("%s scans the whole table; add or extend an index: %s", name, joined)
		}
		if strings.Contains(joined, "TEMP B-TREE FOR ORDER BY") {
			t.Errorf("%s sorts in a TEMP B-TREE; the index must serve ORDER BY created_at DESC: %s", name, joined)
		}
	}
}

// BenchmarkDurationStatsWindow measures the read the statistics endpoint performs over a window with a
// realistic number of tasks, so a cache is only added if the measurement justifies it.
func BenchmarkDurationStatsWindow(b *testing.B) {
	// setupTestDB takes a *testing.T; the benchmark opens its own in-memory database instead.
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		b.Fatalf("open test database: %v", err)
	}
	if err := database.AutoMigrate(&Task{}); err != nil {
		b.Fatalf("migrate: %v", err)
	}
	const rows = 5000
	batch := make([]Task, 0, rows)
	for i := 0; i < rows; i++ {
		batch = append(batch, Task{
			AgentID: "a-1", TenantID: 1, Type: "shell", Command: "whoami",
			Status: "completed", CreatedAt: benchBase(i),
			// updated_at must be AFTER created_at or the endpoint's own filter excludes the row.
			UpdatedAt: benchBase(i).Add(time.Duration(i+1) * time.Millisecond),
		})
	}
	if err := database.CreateInBatches(&batch, 500).Error; err != nil {
		b.Fatalf("seed: %v", err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var out []Task
		if err := database.Select("type, agent_id, status, created_at, updated_at").
			Where("tenant_id = ?", 1).
			Where("status IN ?", []string{"completed", "failed"}).
			Where("created_at >= ?", benchCutoff()).
			Where("updated_at > created_at").
			Order("created_at desc").Limit(5000).Find(&out).Error; err != nil {
			b.Fatalf("query: %v", err)
		}
		if len(out) != rows {
			b.Fatalf("rows = %d, want %d", len(out), rows)
		}
	}
}

// benchBase spreads seeded rows across the last few hours.
func benchBase(i int) time.Time {
	return time.Now().Add(-time.Duration(i) * 30 * time.Second)
}

// benchCutoff is the window start for the benchmark query.
func benchCutoff() time.Time {
	return time.Now().Add(-48 * time.Hour)
}
