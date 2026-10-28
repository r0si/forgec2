package db

import (
	"strings"
	"testing"
)

// TestAgentScopeQueriesUseAnIndex is the same check as the creator one, for the other scoping shape
// the tenant work introduced: `agent_id IN (SELECT id FROM implants WHERE tenant_id = ?)`. It covers
// schedules, status history, the task list and the credentials list the AI tools read per tenant.
func TestAgentScopeQueriesUseAnIndex(t *testing.T) {
	database := setupTestDB(t)
	if err := database.AutoMigrate(&OneShotTask{}, &AgentStatusEvent{}); err != nil {
		t.Fatalf("migrate agent-scoped tables: %v", err)
	}

	queries := map[string]string{
		"one_shot_tasks": `SELECT * FROM one_shot_tasks
			WHERE agent_id IN (SELECT id FROM implants WHERE tenant_id = 1)
			ORDER BY created_at DESC LIMIT 50`,
		"agent_status_events": `SELECT * FROM agent_status_events
			WHERE agent_id IN (SELECT id FROM implants WHERE tenant_id = 1)
			ORDER BY timestamp DESC LIMIT 50`,
		"tasks": `SELECT * FROM tasks
			WHERE agent_id IN (SELECT id FROM implants WHERE tenant_id = 1)
			ORDER BY created_at DESC LIMIT 50`,
		"credential_entries": `SELECT * FROM credential_entries
			WHERE agent_id IN (SELECT id FROM implants WHERE tenant_id = 1) LIMIT 100`,
	}

	for table, query := range queries {
		var plan []struct {
			Detail string `gorm:"column:detail"`
		}
		if err := database.Raw("EXPLAIN QUERY PLAN " + query).Scan(&plan).Error; err != nil {
			t.Fatalf("%s: explain: %v", table, err)
		}
		var details []string
		for _, row := range plan {
			details = append(details, row.Detail)
		}
		joined := strings.Join(details, " | ")
		t.Logf("%s: %s", table, joined)
		if strings.Contains(joined, "SCAN "+table) {
			t.Errorf("%s: the tenant-scoped list scans the whole table: %s", table, joined)
		}
	}
}
