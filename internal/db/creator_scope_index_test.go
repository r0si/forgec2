package db

import (
	"strings"
	"testing"
)

// TestCreatorScopeQueriesUseAnIndex guards the index the tenant scoping depends on.
//
// Rounds 185-200 moved every creator-scoped list onto
// `WHERE created_by IN (SELECT username FROM users WHERE tenant_id = ?) ORDER BY created_at DESC`.
// The four tables had no index on created_by at all, so each of those lists read the whole table and
// sorted it in a TEMP B-TREE - for every tenant-scoped operator, on every page load. SQLite reports
// that in EXPLAIN QUERY PLAN as a SCAN plus "USE TEMP B-TREE FOR ORDER BY", which is what this test
// refuses.
func TestCreatorScopeQueriesUseAnIndex(t *testing.T) {
	// Same helper the rest of this package uses (testutil would be an import cycle). It migrates the
	// models under test, which is what creates the indexes.
	database := setupTestDB(t)
	// The shared helper migrates the core models only; add the four creator-scoped tables so their
	// indexes exist in this schema.
	if err := database.AutoMigrate(&Script{}, &Workflow{}, &PhishingTemplate{}, &PhishingCampaign{}); err != nil {
		t.Fatalf("migrate creator-scoped tables: %v", err)
	}

	queries := map[string]string{
		"scripts": `SELECT * FROM scripts
			WHERE created_by IN (SELECT username FROM users WHERE tenant_id = 1)
			ORDER BY created_at DESC LIMIT 50`,
		"phishing_templates": `SELECT * FROM phishing_templates
			WHERE created_by IN (SELECT username FROM users WHERE tenant_id = 1)
			ORDER BY created_at DESC LIMIT 50`,
		"phishing_campaigns": `SELECT * FROM phishing_campaigns
			WHERE created_by IN (SELECT username FROM users WHERE tenant_id = 1)
			ORDER BY created_at DESC LIMIT 50`,
		"workflows": `SELECT * FROM workflows
			WHERE created_by IN (SELECT username FROM users WHERE tenant_id = 1)
			ORDER BY created_at DESC LIMIT 50`,
	}

	compositeIndex := map[string]string{
		"scripts":            "idx_scripts_creator_time",
		"phishing_templates": "idx_phishing_templates_creator_time",
		"phishing_campaigns": "idx_phishing_campaigns_creator_time",
		"workflows":          "idx_workflows_creator_time",
	}

	for table, query := range queries {
		var plan []struct {
			ID      int    `gorm:"column:id"`
			Parent  int    `gorm:"column:parent"`
			NotUsed int    `gorm:"column:notused"`
			Detail  string `gorm:"column:detail"`
		}
		if err := database.Raw("EXPLAIN QUERY PLAN " + query).Scan(&plan).Error; err != nil {
			t.Fatalf("%s: explain: %v", table, err)
		}
		var details []string
		for _, row := range plan {
			details = append(details, row.Detail)
		}
		joined := strings.Join(details, " | ")
		if strings.Contains(joined, "SCAN "+table) {
			t.Errorf("%s: the tenant-scoped list scans the whole table: %s", table, joined)
		}
		// The plan still ends in "USE TEMP B-TREE FOR ORDER BY": with `created_by IN (SELECT ...)`
		// SQLite uses the composite index for the equality only and then sorts. That is a bounded
		// cost over the creator's own rows, not the whole table, so it is recorded here rather than
		// asserted away - serving the sort would need the query rewritten as a join with
		// ORDER BY created_by, created_at.
		if !strings.Contains(joined, "USING INDEX "+compositeIndex[table]) {
			t.Errorf("%s: the plan does not use %s: %s", table, compositeIndex[table], joined)
		}
	}
}
