package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

// seedTenantMacro creates a macro owned by the given tenant. Steps carry one
// shell step so startMacroRun's parse succeeds.
func seedTenantMacro(t *testing.T, s *Server, name string, tenantID uint) *db.CommandMacro {
	t.Helper()
	steps, err := json.Marshal([]MacroStep{{Command: "whoami", Wait: true}})
	if err != nil {
		t.Fatalf("marshal steps: %v", err)
	}
	macro := &db.CommandMacro{
		Name:     name,
		Steps:    string(steps),
		TenantID: tenantID,
	}
	if err := s.db.Create(macro).Error; err != nil {
		t.Fatalf("seed macro: %v", err)
	}
	return macro
}

// TestRunMacroCrossTenant404 proves a scoped operator cannot run another
// tenant's macro by id (previously a bare First leaked cross-tenant rows).
func TestRunMacroCrossTenant404(t *testing.T) {
	s := mustTenantServer(t)
	macro := seedTenantMacro(t, s, "run-t2", 2)

	c, w := tenantScopedAdminContext(s, t, "macro-run-t1", 1)
	c.Request, _ = http.NewRequest(http.MethodPost, "/", bytes.NewReader([]byte(`{"agent_ids":["a1"]}`)))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "id", Value: strconv.FormatUint(uint64(macro.ID), 10)}}

	s.handleRunMacro(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s, want 404", w.Code, w.Body.String())
	}
	var runs int64
	if err := s.db.Model(&db.MacroRun{}).Count(&runs).Error; err != nil {
		t.Fatalf("count runs: %v", err)
	}
	if runs != 0 {
		t.Fatalf("cross-tenant macro dispatched %d runs", runs)
	}
}

// TestListMacroRunsCrossTenantHidden proves the run list is tenant-scoped:
// tenant 2's runs are invisible to a tenant-1 operator and vice versa.
func TestListMacroRunsCrossTenantHidden(t *testing.T) {
	s := mustTenantServer(t)
	m2 := seedTenantMacro(t, s, "runs-t2", 2)
	if err := s.db.Create(&db.MacroRun{MacroID: m2.ID, MacroName: m2.Name, Status: "running", TenantID: 2, Log: "[]"}).Error; err != nil {
		t.Fatalf("seed run: %v", err)
	}
	m1 := seedTenantMacro(t, s, "runs-t1", 1)
	if err := s.db.Create(&db.MacroRun{MacroID: m1.ID, MacroName: m1.Name, Status: "running", TenantID: 1, Log: "[]"}).Error; err != nil {
		t.Fatalf("seed run: %v", err)
	}

	c, w := tenantScopedAdminContext(s, t, "macro-list-t1", 1)
	c.Request, _ = http.NewRequest(http.MethodGet, "/", nil)
	s.handleListMacroRuns(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200", w.Code)
	}
	var payload struct {
		Runs []db.MacroRun `json:"runs"`
	}
	unmarshalData(t, w.Body.Bytes(), &payload)
	if len(payload.Runs) != 1 {
		t.Fatalf("tenant-1 operator saw %d runs, want only the 1 own-tenant run", len(payload.Runs))
	}
	if payload.Runs[0].TenantID != 1 {
		t.Fatalf("returned run tenant=%d, want 1", payload.Runs[0].TenantID)
	}
}

// TestStopMacroRunCrossTenantRejected proves a scoped operator cannot stop
// another tenant's running macro (they get the same response as a finished
// run, and the foreign row stays running).
func TestStopMacroRunCrossTenantRejected(t *testing.T) {
	s := mustTenantServer(t)
	m2 := seedTenantMacro(t, s, "stop-t2", 2)
	run := db.MacroRun{MacroID: m2.ID, MacroName: m2.Name, Status: "running", TenantID: 2, Log: "[]"}
	if err := s.db.Create(&run).Error; err != nil {
		t.Fatalf("seed run: %v", err)
	}

	c, w := tenantScopedAdminContext(s, t, "macro-stop-t1", 1)
	c.Request, _ = http.NewRequest(http.MethodPost, "/", nil)
	c.Params = gin.Params{{Key: "id", Value: strconv.FormatUint(uint64(run.ID), 10)}}

	s.handleStopMacroRun(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400 (foreign run must not appear stoppable)", w.Code)
	}
	var reloaded db.MacroRun
	if err := s.db.First(&reloaded, run.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.Status != "running" {
		t.Fatalf("cross-tenant stop landed: status=%s", reloaded.Status)
	}
}

// TestStartMacroRunInheritsTenant proves new runs carry the parent macro's
// tenant so future reads stay scoped.
func TestStartMacroRunInheritsTenant(t *testing.T) {
	s := mustTenantServer(t)
	seedTenantAgent(t, s, "macro-agent", 1)
	macro := seedTenantMacro(t, s, "inherit-t1", 1)
	if err := s.startMacroRun(macro, "macro-agent", "inheritor", false); err != nil {
		t.Fatalf("startMacroRun: %v", err)
	}
	var run db.MacroRun
	if err := s.db.Order("id desc").First(&run).Error; err != nil {
		t.Fatalf("load run: %v", err)
	}
	if run.TenantID != 1 {
		t.Fatalf("run tenant=%d, want 1 (inherited from macro)", run.TenantID)
	}
}
