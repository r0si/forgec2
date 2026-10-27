package server

import (
	"strings"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"gorm.io/gorm"
)

// TestAIReportDoesNotLeakAnotherTenant pins two cross-tenant reads that were live in the
// report builder: the MITRE coverage census counted EVERY tenant's task types, and the
// "recent failures" section listed failed tasks from ALL tenants — including the command
// text, which is exactly what a report is meant to summarise without handing to the
// wrong team.
func TestAIReportDoesNotLeakAnotherTenant(t *testing.T) {
	s := mustTenantServer(t)
	seedAIUser(t, s, "viewer1", 1)

	const mine = "e1000000-0000-0000-0000-000000000001"
	const theirs = "e2000000-0000-0000-0000-000000000002"
	if err := s.db.Create(&db.Implant{ID: mine, TenantID: 1, Hostname: "mine", Status: "online", LastSeen: time.Now()}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Create(&db.Implant{ID: theirs, TenantID: 2, Hostname: "theirs", Status: "online", LastSeen: time.Now()}).Error; err != nil {
		t.Fatal(err)
	}

	// A distinctive failed command and type that must not appear in my report.
	if err := s.db.Create(&db.Task{
		AgentID: theirs, TenantID: 2, Type: "atheris_secret_technique",
		Command: "net user admin HUNTER2-PASSWORD /add",
		Status:  "failed", Error: "access denied on the other tenant",
		CreatedAt: time.Now(),
	}).Error; err != nil {
		t.Fatal(err)
	}
	// My own failed task, so the section is not simply empty.
	if err := s.db.Create(&db.Task{
		AgentID: mine, TenantID: 1, Type: "shell", Command: "whoami",
		Status: "failed", Error: "boom", CreatedAt: time.Now(),
	}).Error; err != nil {
		t.Fatal(err)
	}

	report, _, err := s.buildAIMarkdownReport("technical", &aiReqCtx{
		Principal: aiPrincipal{UserID: 1, Username: "viewer1", TenantID: 1, Role: db.RoleAdmin},
	})
	if err != nil {
		t.Fatalf("build report: %v", err)
	}

	for _, secret := range []string{"HUNTER2-PASSWORD", "access denied on the other tenant", "theirs"} {
		if idx := strings.Index(report, secret); idx >= 0 {
			start := idx - 240
			if start < 0 {
				start = 0
			}
			end := idx + 240
			if end > len(report) {
				end = len(report)
			}
			t.Fatalf("the report leaked another tenant's data (%q) near:\n...%s...", secret, report[start:end])
		}
	}
	if !strings.Contains(report, "whoami") && !strings.Contains(report, "boom") {
		t.Fatalf("the report omitted my own failure, so the scoping test proves nothing:\n%s", report)
	}
}

// TestMitreCoverageCountsRespectsScope pins the census half directly: a scoped caller
// must not count task types from agents it cannot see.
func TestMitreCoverageCountsRespectsScope(t *testing.T) {
	s := mustTenantServer(t)
	const mine = "e3000000-0000-0000-0000-000000000003"
	const theirs = "e4000000-0000-0000-0000-000000000004"
	for _, row := range []db.Implant{
		{ID: mine, TenantID: 1, Hostname: "mine", Status: "online", LastSeen: time.Now()},
		{ID: theirs, TenantID: 2, Hostname: "theirs", Status: "online", LastSeen: time.Now()},
	} {
		if err := s.db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := s.db.Create(&db.Task{AgentID: theirs, TenantID: 2, Type: "only_theirs_ran_this", Status: "completed", CreatedAt: time.Now()}).Error; err != nil {
		t.Fatal(err)
	}

	_, _, scopedTypes, err := s.mitreCoverageCounts(func(q *gorm.DB) *gorm.DB {
		return q.Where("agent_id IN (?)", s.db.Model(&db.Implant{}).Select("id").Where("tenant_id = ?", 1))
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, typ := range scopedTypes {
		if typ == "only_theirs_ran_this" {
			t.Fatalf("the scoped census counted another tenant's task type: %v", scopedTypes)
		}
	}

	// The unscoped (legacy single-tenant) view still sees everything.
	_, _, allTypes, err := s.mitreCoverageCounts(nil)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, typ := range allTypes {
		if typ == "only_theirs_ran_this" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the unscoped census lost visibility: %v", allTypes)
	}
}
