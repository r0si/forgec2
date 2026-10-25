package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

// privescCtx builds an operator context (seeded user so the tenant scope
// resolves) with a JSON body.
func privescCtx(s *Server, t *testing.T, username, body string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	c, w := tenantScopedAdminContext(s, t, username, 1)
	c.Request = httptest.NewRequest(http.MethodPost, "/", bytes.NewReader([]byte(body)))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("user_role", "operator")
	var user db.User
	if err := s.db.Where("username = ?", username).First(&user).Error; err != nil {
		t.Fatalf("seed caller: %v", err)
	}
	c.Set("user_id", user.ID)
	return c, w
}

// TestPrivescDispatchCrossTenant404 proves both dispatch endpoints (the
// path-param check plus the body-param run) refuse another tenant's agent.
// handlePrivescRun used to Create the task directly, accepting any agent id
// from any tenant.
func TestPrivescDispatchCrossTenant404(t *testing.T) {
	cases := []struct {
		name   string
		agent  string
		body   string
		invoke func(s *Server, c *gin.Context)
	}{
		{"check", "pv-x", `{"check_type":"all"}`, func(s *Server, c *gin.Context) { s.handlePrivescCheck(c) }},
		{"run", "pv-x", `{"agent_id":"pv-x"}`, func(s *Server, c *gin.Context) { s.handlePrivescRun(c) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := mustTenantServer(t)
			seedTenantAgent(t, s, "pv-x", 2)

			c, w := privescCtx(s, t, "pv-viewer", tc.body)
			c.Params = gin.Params{{Key: "id", Value: tc.agent}}
			tc.invoke(s, c)

			if w.Code != http.StatusNotFound {
				t.Fatalf("status=%d body=%s, want 404 for another tenant's agent", w.Code, w.Body.String())
			}
			assertNoTasksForAgent(t, s, "pv-x")
		})
	}
}

// TestPrivescDispatchSameTenant proves each endpoint queues its task with the
// expected type and command for an own-tenant agent.
func TestPrivescDispatchSameTenant(t *testing.T) {
	s := mustTenantServer(t)
	seedTenantAgent(t, s, "pv-ok", 1)

	c, w := privescCtx(s, t, "pv-op", `{"check_type":"windows"}`)
	c.Params = gin.Params{{Key: "id", Value: "pv-ok"}}
	s.handlePrivescCheck(c)
	if w.Code != http.StatusOK {
		t.Fatalf("check status=%d body=%s", w.Code, w.Body.String())
	}

	c2, w2 := privescCtx(s, t, "pv-op", `{"agent_id":"pv-ok"}`)
	s.handlePrivescRun(c2)
	if w2.Code != http.StatusOK {
		t.Fatalf("run status=%d body=%s", w2.Code, w2.Body.String())
	}

	var checks int64
	if err := s.db.Model(&db.Task{}).Where("type = ?", "privesc_check").Count(&checks).Error; err != nil {
		t.Fatalf("count checks: %v", err)
	}
	if checks != 2 {
		t.Fatalf("privesc_check tasks = %d, want 2 (check + run)", checks)
	}
	var task db.Task
	if err := s.db.Where("type = ?", "privesc_check").Order("id asc").First(&task).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if task.Command != "privesc_check:windows" {
		t.Fatalf("command = %q, want privesc_check:windows", task.Command)
	}
}

// TestPrivescDispatchRejectsMissingFields proves the body-param endpoint
// still validates its inputs (the refactor must not lose the 400).
func TestPrivescDispatchRejectsMissingFields(t *testing.T) {
	s := mustTenantServer(t)
	seedTenantAgent(t, s, "pv-val", 1)

	c, w := privescCtx(s, t, "pv-op", `{"check_type":"all"}`)
	s.handlePrivescRun(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("run without agent_id: status=%d, want 400", w.Code)
	}
	assertNoTasksForAgent(t, s, "pv-val")
}

// TestProcessPrivescResultFormatsAndCompletes proves the result handler parses
// the agent payload, formats the report text and marks the task completed.
func TestProcessPrivescResultFormatsAndCompletes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)
	if err := s.db.Create(&db.Task{AgentID: "pv-res", Type: "privesc_check", Status: "running"}).Error; err != nil {
		t.Fatalf("seed task: %v", err)
	}

	body := `{"task_id":1,"agent_id":"pv-res","os":"windows","vulnerabilities":[{"cve":"CVE-2021-4034","title":"PwnKit","description":"pkexec LPE","severity":"high"}],"misconfigurations":[{"title":"WeakACL","description":"C:\\ writable","severity":"medium"}],"suggestions":["patch"]}`
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/", bytes.NewReader([]byte(body)))
	c.Request.Header.Set("Content-Type", "application/json")
	s.handleProcessPrivescResult(c)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var task db.Task
	if err := s.db.First(&task, 1).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if task.Status != "completed" {
		t.Fatalf("status = %q, want completed", task.Status)
	}
	for _, want := range []string{"PwnKit", "CVE-2021-4034", "WeakACL", "patch", "1"} {
		if !bytes.Contains([]byte(task.Result), []byte(want)) {
			t.Fatalf("formatted result missing %q: %s", want, task.Result)
		}
	}
}

// TestProcessPrivescResultRejectsBadJSON proves a malformed payload is a 400,
// not a silent 200 that leaves the task "running" forever.
func TestProcessPrivescResultRejectsBadJSON(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/", bytes.NewReader([]byte("not json")))
	c.Request.Header.Set("Content-Type", "application/json")
	s.handleProcessPrivescResult(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400", w.Code)
	}
}

// TestPrivescHistoryEndpoint proves the history endpoint returns the stored
// task with its findings count, and 404s for a non-privesc task id.
func TestPrivescHistoryEndpoint(t *testing.T) {
	s := mustTenantServer(t)
	if err := s.db.Create(&db.Task{
		ID: 7, AgentID: "pv-hist", Type: "privesc_check",
		Command: "privesc_check:linux", Status: "completed", Result: "[!] sudo WRITABLE",
	}).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	// A non-privesc task must not be reachable through this endpoint.
	if err := s.db.Create(&db.Task{ID: 8, AgentID: "pv-hist", Type: "shell", Status: "completed"}).Error; err != nil {
		t.Fatalf("seed shell task: %v", err)
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	c.Params = gin.Params{{Key: "id", Value: "7"}}
	s.handlePrivescHistory(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s, want 200", w.Code, w.Body.String())
	}
	var payload struct {
		Tasks    []privescHistoryEntry `json:"tasks"`
		Findings []gin.H               `json:"findings"`
		Total    int                   `json:"total"`
	}
	unmarshalData(t, w.Body.Bytes(), &payload)
	if payload.Total != 1 || len(payload.Tasks) != 1 {
		t.Fatalf("payload = %+v, want one task", payload)
	}
	if payload.Tasks[0].CheckType != "linux" || payload.Tasks[0].FindingsCount != 1 {
		t.Fatalf("task summary = %+v, want linux/1 finding", payload.Tasks[0])
	}

	w2 := httptest.NewRecorder()
	c2, _ := gin.CreateTestContext(w2)
	c2.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	c2.Params = gin.Params{{Key: "id", Value: "8"}}
	s.handlePrivescHistory(c2)
	if w2.Code != http.StatusNotFound {
		t.Fatalf("status=%d, want 404 for a non-privesc task", w2.Code)
	}
}

// TestBuildPrivescFindingsAggregates proves findings from several tasks are
// concatenated in order.
func TestBuildPrivescFindingsAggregates(t *testing.T) {
	s := mustTenantServer(t)
	tasks := []db.Task{
		{ID: 1, Result: "[!] first vuln"},
		{ID: 2, Result: "clean output"},
		{ID: 3, Result: "[!] third vuln"},
	}
	got := s.buildPrivescFindings(tasks)
	if len(got) != 2 {
		t.Fatalf("findings = %d, want 2 (the clean task contributes none)", len(got))
	}
}

// TestParsePrivescFindings pins the finding extractor: CVE section headers
// attribute later findings, severity is derived from the markers, and
// non-finding lines are ignored.
func TestParsePrivescFindings(t *testing.T) {
	s := mustTenantServer(t)

	none := s.parsePrivescFindings("all clear\nnothing to see")
	if len(none) != 0 {
		t.Fatalf("clean output produced %d findings", len(none))
	}

	text := "--- CVE-2021-4034 (pkexec/pwnkit) ---\n[!] pkexec writable\nSUDO entry WRITABLE\ninfo line\n"
	got := s.parsePrivescFindings(text)
	if len(got) != 2 {
		t.Fatalf("findings = %d, want 2 (blank and info lines ignored)", len(got))
	}
	first := got[0]
	if first["cve_id"] != "CVE-2021-4034" {
		t.Fatalf("cve attribution lost: %v", first)
	}
	if title, _ := first["title"].(string); title == "" || !bytes.Contains([]byte(title), []byte("CVE-2021-4034")) {
		t.Fatalf("title missing the CVE prefix: %v", first)
	}
	if first["severity"] != "high" {
		t.Fatalf("severity = %v, want high for a plain [!] finding", first["severity"])
	}
	if got[1]["severity"] != "medium" {
		t.Fatalf("WRITABLE severity = %v, want medium", got[1]["severity"])
	}
}

// TestBuildPrivescHistoryMapsCheckType proves the history mapping derives the
// check type from the command and counts findings.
func TestBuildPrivescHistoryMapsCheckType(t *testing.T) {
	s := mustTenantServer(t)

	tasks := []db.Task{
		{ID: 1, AgentID: "a", Command: "privesc_check:windows", Status: "completed", Result: "[!] vuln"},
		{ID: 2, AgentID: "a", Command: "privesc_execute", Status: "completed", Result: ""},
	}
	out := s.buildPrivescHistory(tasks)
	if len(out) != 2 {
		t.Fatalf("history len = %d", len(out))
	}
	if out[0].CheckType != "windows" {
		t.Fatalf("check type = %q, want windows", out[0].CheckType)
	}
	if out[1].CheckType != "all" {
		t.Fatalf("non-check command mapped to %q, want the default all", out[1].CheckType)
	}
	if out[0].FindingsCount != 1 || out[1].FindingsCount != 0 {
		t.Fatalf("findings counts wrong: %d/%d", out[0].FindingsCount, out[1].FindingsCount)
	}
}
