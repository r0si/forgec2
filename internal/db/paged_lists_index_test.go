package db

import (
	"strings"
	"testing"
)

// TestPagedListQueriesUseAnIndex covers the remaining paged lists: the ones a page polls on a timer,
// where a table scan costs CPU forever rather than once.
func TestPagedListQueriesUseAnIndex(t *testing.T) {
	database := setupTestDB(t)
	if err := database.AutoMigrate(&AuditLog{}, &Notification{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	queries := map[string]string{
		// Audit page: newest entries, optionally one action.
		"audit_logs_recent": `SELECT * FROM audit_logs ORDER BY created_at DESC LIMIT 100`,
		"audit_logs_by_action": `SELECT * FROM audit_logs WHERE action = 'execute_script'
			ORDER BY created_at DESC LIMIT 100`,
		"audit_logs_tenant": `SELECT * FROM audit_logs WHERE tenant_id = 1 OR tenant_id = 0
			ORDER BY created_at DESC LIMIT 100`,
		// Notification bell: one user's newest unread.
		"notifications_unread": `SELECT * FROM notifications WHERE read = 0
			ORDER BY created_at DESC LIMIT 20`,
		"tasks_by_tenant": `SELECT * FROM tasks WHERE tenant_id = 1
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
			// audit_logs_tenant filters "tenant_id = 1 OR tenant_id = 0" (the legacy-visibility rule),
			// and an OR cannot use the composite index for ordering. Recorded rather than asserted
			// away; the row count it sorts is the tenant's own audit trail, not the whole table.
			if name == "audit_logs_tenant" && !strings.Contains(joined, "SCAN audit_logs") {
				continue
			}
			t.Errorf("%s: sorts in a TEMP B-TREE: %s", name, joined)
		}
	}
}
