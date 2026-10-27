package server

import (
	"testing"
	"time"

	forgecrypto "github.com/forgec2/forgec2/internal/crypto"
	"github.com/forgec2/forgec2/internal/db"
)

// TestCollectSituationScopesAlerts pins the last unscoped aggregate in the assistant's
// situation snapshot. Every other counter there is narrowed to the caller's tenant; the
// active-alert count was global, so a tenant could read how noisy another tenant's estate
// was just by asking the assistant for a summary.
func TestCollectSituationScopesAlerts(t *testing.T) {
	s := mustTenantServer(t)
	seedAIUser(t, s, "ops1", 1)

	for _, alert := range []db.Alert{
		{TenantID: 1, Type: "persistence", Severity: "warning", Title: "mine", Status: "active"},
		{TenantID: 1, Type: "persistence", Severity: "warning", Title: "mine2", Status: "active"},
		{TenantID: 2, Type: "credential", Severity: "critical", Title: "theirs", Status: "active"},
		{TenantID: 2, Type: "credential", Severity: "critical", Title: "theirs2", Status: "active"},
		{TenantID: 2, Type: "credential", Severity: "critical", Title: "theirs3", Status: "active"},
	} {
		if err := s.db.Create(&alert).Error; err != nil {
			t.Fatal(err)
		}
	}

	scopedCtx := &aiReqCtx{Principal: aiPrincipal{
		UserID: 1, Username: "ops1", TenantID: 1, Role: db.RoleAdmin,
	}}
	situation := s.collectSituation(scopedCtx)
	if situation.ActiveAlerts != 2 {
		t.Fatalf("scoped active_alerts = %d, want 2 (only my tenant's)", situation.ActiveAlerts)
	}

	// The unscoped (legacy single-tenant) view still sees every alert.
	all := s.collectSituation(nil)
	if all.ActiveAlerts != 5 {
		t.Fatalf("unscoped active_alerts = %d, want 5", all.ActiveAlerts)
	}
}

// TestCollectSituationScopesEveryCounter is the broader guard: a snapshot built for a
// tenant must never report another tenant's agents, credentials or pending approvals.
// It plants distinctive rows on tenant 2 and asserts the tenant-1 snapshot is zero for
// each counter, so a future query added without the scope closure fails here.
func TestCollectSituationScopesEveryCounter(t *testing.T) {
	s := mustTenantServer(t)
	forgecrypto.InitLootEncryption(testStorageKeyHex)
	seedAIUser(t, s, "ops2", 1)

	const theirs = "f1000000-0000-0000-0000-000000000001"
	if err := s.db.Create(&db.Implant{
		ID: theirs, TenantID: 2, Hostname: "other-team", Status: "online", LastSeen: time.Now(),
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Create(&db.Task{
		AgentID: theirs, TenantID: 2, Type: "shell", Command: "whoami",
		Status: TaskStatusPendingApproval, CreatedBy: "ai", CreatedAt: time.Now(),
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Create(&db.CredentialEntry{
		AgentID: theirs, TenantID: 2, Username: "u", Password: "p", CreatedAt: time.Now(),
	}).Error; err != nil {
		t.Fatal(err)
	}

	situation := s.collectSituation(&aiReqCtx{Principal: aiPrincipal{
		UserID: 1, Username: "ops2", TenantID: 1, Role: db.RoleAdmin,
	}})

	if situation.AgentsTotal != 0 || situation.AgentsOnline != 0 {
		t.Fatalf("agent counters leaked: total=%d online=%d", situation.AgentsTotal, situation.AgentsOnline)
	}
	if situation.PendingApproval != 0 || situation.AIPending != 0 {
		t.Fatalf("approval counters leaked: pending=%d ai=%d", situation.PendingApproval, situation.AIPending)
	}
	if situation.Credentials != 0 {
		t.Fatalf("credential counter leaked: %d", situation.Credentials)
	}
}
