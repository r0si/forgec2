package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// TestScriptGlobalsAreTenantScoped pins the fix for a data leak into the scripting sandbox: the
// agents/tasks/credentials globals handed to a script were queried WITHOUT a tenant filter, so
// an admin scoped to one tenant still received every other tenant's agents, task commands and
// credential identities. The optional single-target narrowing was already tenant-scoped, which
// is what made the leak easy to miss.
//
// Note on how values arrive in the VM: goja exposes Go structs under their EXPORTED field names
// (Hostname, not hostname). My first version of this test read the lowercased names, got [null]
// from a one-element slice, and "passed" while the leak was live.
func TestScriptGlobalsAreTenantScoped(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)

	if err := s.db.Create(&db.Implant{
		ID: "tenant-one-host", Hostname: "sekrit-host", IP: "10.9.9.9", OS: "windows", Status: "online", TenantID: 1, LastSeen: time.Now(),
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Create(&db.CredentialEntry{AgentID: "tenant-one-host", Domain: "SEKRITDOM", Username: "sekrit-user", Type: "password"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Create(&db.Task{AgentID: "tenant-one-host", Type: "shell", Command: "sekrit-command", Status: "completed"}).Error; err != nil {
		t.Fatal(err)
	}

	code := `JSON.stringify({` +
		`agents: agents.map(a => a.Hostname), ` +
		`tasks: tasks.map(t => t.Command), ` +
		`creds: credentials.map(c => c.Username), ` +
		`n: agents.length})`
	payload, err := json.Marshal(map[string]string{"code": code})
	if err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/scripting/execute", bytes.NewBuffer(payload))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("user", "tenant2-admin")
	c.Set("user_role", db.RoleAdmin)
	c.Set("user_id", uint(9))
	c.Set(middleware.TenantIDContextKey, uint(2))

	s.handleAPIExecuteScript(c)

	resp := w.Body.String()
	if w.Code != http.StatusOK {
		t.Fatalf("execute = %d (%s)", w.Code, resp)
	}
	for _, leak := range []string{"sekrit-host", "sekrit-command", "sekrit-user", "SEKRITDOM"} {
		if bytes.Contains([]byte(resp), []byte(leak)) {
			t.Fatalf("tenant 2's script context leaked %q: %s", leak, resp)
		}
	}
	// The count is the unambiguous signal: without the tenant scope the slice holds the
	// foreign row, and the map above renders it as [null] if a field name is wrong.
	if !bytes.Contains([]byte(resp), []byte(`n\":0`)) && !bytes.Contains([]byte(resp), []byte(`n":0`)) {
		t.Fatalf("the agents global was not empty for tenant 2: %s", resp)
	}
}
