package db

import (
	"strings"
	"testing"
)

// TestHotAgentTaskQueriesUseAnIndex covers the per-agent list shapes, which are the hottest reads in
// the console: the beacon detail page asks for one agent's tasks, and the timeline asks for its status
// history, both ordered by time.
func TestHotAgentTaskQueriesUseAnIndex(t *testing.T) {
	database := setupTestDB(t)
	if err := database.AutoMigrate(&AgentStatusEvent{}, &OneShotTask{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	queries := map[string]string{
		"tasks_by_agent": `SELECT * FROM tasks WHERE agent_id = 'abc'
			ORDER BY created_at DESC LIMIT 50`,
		"tasks_by_agent_status": `SELECT * FROM tasks WHERE agent_id = 'abc' AND status = 'pending'
			ORDER BY created_at DESC LIMIT 50`,
		"status_events_by_agent": `SELECT * FROM agent_status_events WHERE agent_id = 'abc'
			ORDER BY timestamp DESC LIMIT 50`,
		"one_shot_by_agent": `SELECT * FROM one_shot_tasks WHERE agent_id = 'abc'
			ORDER BY created_at DESC LIMIT 50`,
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
		if strings.Contains(joined, "TEMP B-TREE FOR ORDER BY") {
			t.Errorf("%s: sorts in a TEMP B-TREE, so the page reads every row of the agent first: %s", name, joined)
		}
	}
}
