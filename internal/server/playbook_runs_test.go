package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/playbook"
	"github.com/forgec2/forgec2/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// TestPlaybookRunPersistence covers save, list and read for playbook runs.
//
// The state machine is tested exhaustively in internal/playbook; what this covers is the join between it and storage - that a run
// survives the round trip, that the stored State column agrees with the value the machine computes, and that the read view reports
// what may run next without storing a second copy of that answer.
func TestPlaybookRunPersistence(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)

	call := func(method, path string, tenant uint) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(method, path, nil)
		parts := strings.Split(strings.TrimPrefix(path, "/api/playbooks/runs"), "/")
		if len(parts) > 1 && parts[1] != "" {
			c.Params = gin.Params{{Key: "id", Value: parts[1]}}
		}
		c.Set("user", "op")
		c.Set("user_role", db.RoleAdmin)
		c.Set("user_id", uint(1))
		c.Set(middleware.TenantIDContextKey, tenant)
		if len(c.Params) > 0 {
			s.handleGetPlaybookRun(c)
		} else {
			s.handleListPlaybookRuns(c)
		}
		return w
	}

	// 1. A half-finished run is saved, and its stored state matches the machine.
	run := playbook.Run{Name: "AD path", Steps: []playbook.Step{
		{ID: "recon"},
		{ID: "exploit", Needs: []string{"recon"}},
		{ID: "exfil", Needs: []string{"exploit"}, Approval: true},
	}}
	if err := run.Normalise(); err != nil {
		t.Fatalf("normalise: %v", err)
	}
	if err := run.Start("recon"); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := run.Succeed("recon"); err != nil {
		t.Fatalf("succeed: %v", err)
	}
	record, err := s.savePlaybookRun(playbookTenantContext(1), run, "op")
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if record.State != "blocked" {
		t.Fatalf("stored state = %q, want blocked (the approval step is waiting)", record.State)
	}
	run.ID = uitoa(record.ID)
	if _, err := s.savePlaybookRun(playbookTenantContext(1), run, "op"); err != nil {
		t.Fatalf("re-save: %v", err)
	}

	// 2. The list reports progress without opening the run.
	list := call(http.MethodGet, "/api/playbooks/runs", 1)
	if list.Code != http.StatusOK {
		t.Fatalf("list = %d (%s)", list.Code, list.Body.String())
	}
	var listed struct {
		Data struct {
			Count int `json:"count"`
			Runs  []struct {
				ID      uint   `json:"id"`
				State   string `json:"state"`
				Settled int    `json:"settled"`
				Total   int    `json:"total"`
			} `json:"runs"`
		} `json:"data"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if listed.Data.Count != 1 || listed.Data.Runs[0].Settled != 1 || listed.Data.Runs[0].Total != 3 {
		t.Fatalf("list = %+v", listed.Data)
	}

	// 3. The detail names what may run next, and nothing that is waiting for a human.
	detail := call(http.MethodGet, "/api/playbooks/runs/"+uitoa(record.ID), 1)
	if detail.Code != http.StatusOK {
		t.Fatalf("detail = %d (%s)", detail.Code, detail.Body.String())
	}
	var shown struct {
		Data struct {
			State   string          `json:"state"`
			Ready   []playbook.Step `json:"ready"`
			Settled int             `json:"settled"`
			Steps   []playbook.Step `json:"steps"`
		} `json:"data"`
	}
	if err := json.Unmarshal(detail.Body.Bytes(), &shown); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	if shown.Data.State != "blocked" {
		t.Fatalf("detail state = %q", shown.Data.State)
	}
	if len(shown.Data.Ready) != 1 || shown.Data.Ready[0].ID != "exploit" {
		t.Fatalf("ready = %+v, want [exploit]", shown.Data.Ready)
	}
	if len(shown.Data.Steps) != 3 {
		t.Fatalf("steps = %d, want the three", len(shown.Data.Steps))
	}

	// 4. Another tenant cannot see it.
	if other := call(http.MethodGet, "/api/playbooks/runs/"+uitoa(record.ID), 2); other.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant read = %d, want 404", other.Code)
	}
	if otherList := call(http.MethodGet, "/api/playbooks/runs", 2); otherList.Code != http.StatusOK {
		t.Fatalf("cross-tenant list = %d", otherList.Code)
	} else if !strings.Contains(otherList.Body.String(), `"count":0`) {
		t.Fatalf("tenant 2 sees another tenant's runs: %s", otherList.Body.String())
	}

	// 5. A malformed id is a 400, not a 500.
	if bad := call(http.MethodGet, "/api/playbooks/runs/notanumber", 1); bad.Code != http.StatusBadRequest {
		t.Fatalf("malformed id = %d, want 400", bad.Code)
	}
}

// playbookTenantContext builds a gin context bound to one tenant, for the storage helpers that take a context rather than a request.
func playbookTenantContext(tenant uint) *gin.Context {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Set("user", "op")
	c.Set("user_role", db.RoleAdmin)
	c.Set("user_id", uint(1))
	c.Set(middleware.TenantIDContextKey, tenant)
	return c
}
