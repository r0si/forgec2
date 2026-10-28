package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// TestWorkflowsAreTenantScoped pins the scope on executable automation. A workflow CREATES TASKS
// on the agents it targets, so the unscoped execute was the worst instance of this family: a
// tenant-2 operator could run tenant-1's workflow against tenant-1's implants, and the target
// resolution (ids, tags, groups, "recent") queried every tenant as well.
func TestWorkflowsAreTenantScoped(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)

	if err := s.db.Create(&db.User{Username: "tenant1-admin", Role: db.RoleAdmin, TenantID: 1, IsActive: true}).Error; err != nil {
		t.Fatal(err)
	}
	victimAgent := db.Implant{ID: "wf-victim-agent", Hostname: "victim-host", Status: "online", TenantID: 1, LastSeen: time.Now()}
	if err := s.db.Create(&victimAgent).Error; err != nil {
		t.Fatal(err)
	}
	wf := db.Workflow{
		ID: "wf-victim", Name: "victim-flow", Enabled: true, ScopeType: "agents",
		ScopeIDs:  `["` + victimAgent.ID + `"]`,
		CreatedBy: "tenant1-admin", CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := s.db.Create(&wf).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Create(&db.WorkflowStep{WorkflowID: wf.ID, StepOrder: 1, TaskType: "shell", Command: "whoami"}).Error; err != nil {
		t.Fatal(err)
	}

	ctxFor := func(method, target string) (*gin.Context, *httptest.ResponseRecorder) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(method, target, bytes.NewReader(nil))
		c.Params = gin.Params{{Key: "id", Value: wf.ID}}
		c.Set("user", "tenant2-admin")
		c.Set("user_role", db.RoleAdmin)
		c.Set("user_id", uint(9))
		c.Set(middleware.TenantIDContextKey, uint(2))
		return c, w
	}

	// It is not listed.
	c, w := ctxFor(http.MethodGet, "/api/workflows")
	s.handleAPIWorkflows(c)
	if bytes.Contains(w.Body.Bytes(), []byte("victim-flow")) {
		t.Fatalf("tenant 2 listed tenant 1's workflow: %s", w.Body.String())
	}

	// It cannot be toggled, and the row is unchanged.
	before := db.Workflow{}
	// String primary keys must be passed as a CONDITION: First(&x, "wf-victim") treats the string
	// as raw SQL and SQLite reads the first token as a column name.
	if err := s.db.Where("id = ?", wf.ID).First(&before).Error; err != nil {
		t.Fatal(err)
	}
	c, w = ctxFor(http.MethodPost, "/api/workflows/"+wf.ID+"/toggle")
	s.handleAPIWorkflowsToggle(c)
	after := db.Workflow{}
	if err := s.db.Where("id = ?", wf.ID).First(&after).Error; err != nil {
		t.Fatal(err)
	}
	if after.Enabled != before.Enabled {
		t.Fatalf("tenant 2 toggled tenant 1's workflow (enabled %v -> %v)", before.Enabled, after.Enabled)
	}

	// It cannot be executed - and in particular no task is created on the foreign agent.
	c, w = ctxFor(http.MethodPost, "/api/workflows/"+wf.ID+"/execute")
	s.handleAPIWorkflowsExecute(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("tenant 2 executed tenant 1's workflow: %d (%s)", w.Code, w.Body.String())
	}
	var tasks int64
	if err := s.db.Model(&db.Task{}).Where("agent_id = ?", victimAgent.ID).Count(&tasks).Error; err != nil {
		t.Fatal(err)
	}
	if tasks != 0 {
		t.Fatalf("%d task(s) were created on the foreign agent", tasks)
	}

	// It cannot be deleted.
	c, w = ctxFor(http.MethodDelete, "/api/workflows/"+wf.ID)
	s.handleAPIDeleteWorkflow(c)
	var remaining int64
	if err := s.db.Model(&db.Workflow{}).Where("id = ?", wf.ID).Count(&remaining).Error; err != nil {
		t.Fatal(err)
	}
	if remaining != 1 {
		t.Fatal("tenant 2 deleted tenant 1's workflow")
	}
}
