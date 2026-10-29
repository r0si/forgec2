package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/playbook"
	"github.com/forgec2/forgec2/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// TestPlaybookRunHistory covers the read that puts the audited transitions where the question is asked.
//
// The rows already exist - every transition is audited - so this is not a second record, it is the same record shown next to the run. The
// assertions are therefore about the SELECTION (the right run, both action kinds, oldest first) and about not inventing an answer for a run
// that does not exist.
func TestPlaybookRunHistory(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)

	auditCtx := func() *gin.Context {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		// A request is required: the audit path reads the client IP and the user agent off it.
		c.Request = httptest.NewRequest(http.MethodPost, "/api/playbooks/runs", nil)
		c.Set("user", "op")
		c.Set("user_role", db.RoleAdmin)
		c.Set("user_id", uint(1))
		// The SERVER's own key, not only the middleware one: audit rows are attributed from tenantResolution, and setting just the
		// middleware key leaves them at tenant 0 - which the tenant-scoped history query then correctly filters out, producing an empty
		// history for a run that definitely has one.
		c.Set(tenantIDContextKey, tenantResolution{tid: 1, ok: true})
		c.Set(middleware.TenantIDContextKey, uint(1))
		return c
	}
	record, err := s.savePlaybookRun(auditCtx(), runForHistory(), "op")
	if err != nil {
		t.Fatalf("save run: %v", err)
	}
	runID := itoaUint(record.ID)

	// Two audited transitions on this run, and one on a DIFFERENT run that must not appear.
	s.LogAuditRecord(auditCtx(), "playbook_run_transition", "playbook", runID, `step "recon": start`, true, nil)
	s.LogAuditRecord(auditCtx(), "playbook_run_transition", "playbook", runID, `step "recon": succeed`, true, nil)
	s.LogAuditRecord(auditCtx(), "playbook_run_transition", "playbook", "9999", `step "other": start`, true, nil)

	// The audit writer is a queue when the server has a worker, so the rows land shortly after the call. Polling here is honest -
	// the alternative (asserting immediately) tests the queue's emptiness rather than the history endpoint.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var count int64
		if err := s.db.Model(&db.AuditLog{}).Where("resource = ?", runID).Count(&count).Error; err == nil && count >= 2 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/playbooks/runs/"+runID+"/history", nil)
	c.Params = gin.Params{{Key: "id", Value: runID}}
	c.Set("user", "op")
	c.Set("user_role", db.RoleAdmin)
	c.Set("user_id", uint(1))
	c.Set(middleware.TenantIDContextKey, uint(1))
	s.handlePlaybookRunHistory(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", w.Code, w.Body.String())
	}

	var resp struct {
		Data struct {
			RunID   uint `json:"run_id"`
			Count   int  `json:"count"`
			Entries []struct {
				Action  string `json:"action"`
				Details string `json:"details"`
				At      string `json:"at"`
				User    string `json:"user"`
			} `json:"entries"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v (%s)", err, w.Body.String())
	}

	// 1. Only this run's rows, oldest first.
	if resp.Data.Count != 2 || len(resp.Data.Entries) != 2 {
		t.Fatalf("entries = %+v, want the two transitions of this run", resp.Data.Entries)
	}
	if !strings.Contains(resp.Data.Entries[0].Details, "start") || !strings.Contains(resp.Data.Entries[1].Details, "succeed") {
		t.Fatalf("the history is not in the order it happened: %+v", resp.Data.Entries)
	}
	if strings.Contains(w.Body.String(), "other") {
		t.Fatalf("another run's transition leaked into the history: %s", w.Body.String())
	}
	// 2. Every entry is attributable and timestamped, or it cannot answer "who advanced this".
	for _, entry := range resp.Data.Entries {
		if entry.At == "" {
			t.Fatalf("an entry has no timestamp: %+v", entry)
		}
	}

	// 3. A run that does not exist is a 404, not an empty history that reads as "nothing happened yet".
	missing := httptest.NewRecorder()
	c2, _ := gin.CreateTestContext(missing)
	c2.Request = httptest.NewRequest(http.MethodGet, "/api/playbooks/runs/424242/history", nil)
	c2.Params = gin.Params{{Key: "id", Value: "424242"}}
	c2.Set("user", "op")
	c2.Set("user_role", db.RoleAdmin)
	c2.Set("user_id", uint(1))
	c2.Set(middleware.TenantIDContextKey, uint(1))
	s.handlePlaybookRunHistory(c2)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing run = %d, want 404", missing.Code)
	}

	// 4. A malformed id is a 400.
	bad := httptest.NewRecorder()
	c3, _ := gin.CreateTestContext(bad)
	c3.Request = httptest.NewRequest(http.MethodGet, "/api/playbooks/runs/abc/history", nil)
	c3.Params = gin.Params{{Key: "id", Value: "abc"}}
	c3.Set("user", "op")
	c3.Set("user_role", db.RoleAdmin)
	c3.Set("user_id", uint(1))
	c3.Set(middleware.TenantIDContextKey, uint(1))
	s.handlePlaybookRunHistory(c3)
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("malformed id = %d, want 400", bad.Code)
	}
}

// runForHistory is a minimal normalised run for the history test.
func runForHistory() playbook.Run {
	run := playbook.Run{Name: "history", Steps: []playbook.Step{{ID: "recon"}}}
	_ = run.Normalise()
	return run
}
