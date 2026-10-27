package server

import (
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/scripting"
)

// TestScriptingBridgeEnforcesCallerPermissions pins the invariant behind the whole
// scripting feature: script code runs with the CALLER's privileges, never more.
// A script executed on behalf of a low-privilege operator must not be able to send
// tasks or read agents, and the bridge is the only thing enforcing that.
func TestScriptingBridgeEnforcesCallerPermissions(t *testing.T) {
	s := mustTenantServer(t)
	b := &scriptingBridge{s: s}

	agent := db.Implant{ID: "44444444-4444-4444-4444-444444444444", TenantID: 1, Hostname: "h", Status: "online", LastSeen: time.Now()}
	if err := s.db.Create(&agent).Error; err != nil {
		t.Fatal(err)
	}

	// A role with no permissions at all.
	nobody := scripting.Caller{Username: "nobody", Role: "no-such-role", TenantID: 1}
	if _, err := b.SendTask(nobody, agent.ID, "shell", "whoami"); err == nil {
		t.Fatal("a permissionless caller created a task through the bridge")
	}
	if got, err := b.GetAgent(nobody, agent.ID); err == nil || got != nil {
		t.Fatalf("a permissionless caller read an agent: %v %v", got, err)
	}
	if list, err := b.ListAgents(nobody); err == nil && len(list) > 0 {
		t.Fatalf("a permissionless caller listed %d agents", len(list))
	}
	if _, err := b.Query(nobody, "agents", nil); err == nil {
		t.Fatal("a permissionless caller ran a query")
	}
	if _, err := b.HTTPRequest(nobody, "GET", "https://example.com", nil, "", 1); err == nil {
		t.Fatal("a permissionless caller made an HTTP request")
	}

	// An admin passes every gate (the agent belongs to their tenant).
	admin := scripting.Caller{Username: "admin1", Role: db.RoleAdmin, TenantID: 1}
	if _, err := b.SendTask(admin, agent.ID, "shell", "whoami"); err != nil {
		t.Fatalf("an admin was refused: %v", err)
	}
	if got, err := b.GetAgent(admin, agent.ID); err != nil || got == nil {
		t.Fatalf("an admin could not read the agent: %v %v", got, err)
	}
}

// TestScriptingBridgeScopesToCallerTenant pins the tenant boundary: even a caller
// with the right permission must not reach another tenant's agent, and the refusal
// is "not found" rather than an error that confirms the agent exists.
func TestScriptingBridgeScopesToCallerTenant(t *testing.T) {
	s := mustTenantServer(t)
	b := &scriptingBridge{s: s}

	foreign := db.Implant{ID: "55555555-5555-5555-5555-555555555555", TenantID: 2, Hostname: "other", Status: "online", LastSeen: time.Now()}
	if err := s.db.Create(&foreign).Error; err != nil {
		t.Fatal(err)
	}

	caller := scripting.Caller{Username: "admin1", Role: db.RoleAdmin, TenantID: 1}
	if _, err := b.SendTask(caller, foreign.ID, "shell", "whoami"); err == nil {
		t.Fatal("a task was queued on another tenant's agent")
	}
	if _, err := b.GetAgent(caller, foreign.ID); err == nil {
		t.Fatal("another tenant's agent was readable")
	}

	// An unscoped caller (TenantID 0, system paths) still sees it: that is the
	// legacy single-tenant contract.
	unscoped := scripting.Caller{Username: "system", Role: db.RoleAdmin}
	if _, err := b.GetAgent(unscoped, foreign.ID); err != nil {
		t.Fatalf("an unscoped caller lost access: %v", err)
	}
}

// TestScriptingBridgeAttributesScriptTasks pins the approval-flow fix: a task
// created by a script carries the caller as created_by. Without it an empty
// creator made the task approvable by anyone, so a lone operator could create and
// self-approve a dangerous task through a script.
func TestScriptingBridgeAttributesScriptTasks(t *testing.T) {
	s := mustTenantServer(t)
	b := &scriptingBridge{s: s}

	agent := db.Implant{ID: "66666666-6666-6666-6666-666666666666", TenantID: 1, Hostname: "h", Status: "online", LastSeen: time.Now()}
	if err := s.db.Create(&agent).Error; err != nil {
		t.Fatal(err)
	}

	caller := scripting.Caller{Username: "operator-7", Role: db.RoleAdmin, TenantID: 1}
	id, err := b.SendTask(caller, agent.ID, "shell", "whoami")
	if err != nil {
		t.Fatalf("SendTask: %v", err)
	}
	var task db.Task
	if err := s.db.First(&task, id).Error; err != nil {
		t.Fatal(err)
	}
	if task.CreatedBy != "operator-7" {
		t.Fatalf("created_by = %q, want the scripting caller", task.CreatedBy)
	}
}
