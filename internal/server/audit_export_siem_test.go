package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// TestAuditExportSIEMProjections covers ?format=ecs and ?format=cef.
//
// The NDJSON export is the verifiable artefact; these two are PROJECTIONS for a SIEM, and the whole feature exists because "I
// exported it to the SIEM, so it is preserved" is a false assumption - a collector normalises documents and would silently break
// any chain rebuilt from its copy. So each response must say, in a header, that it cannot verify the log.
func TestAuditExportSIEMProjections(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)

	// A row whose details contain the CEF delimiters: a pipe in the header grammar and an equals in the extension grammar.
	if err := s.db.Create(&db.AuditLog{
		TenantID: 1, User: "op", Action: "task_create", Resource: "a|b",
		AgentID: "a-1", IP: "10.0.0.5", Success: true,
		Details: "cmd=whoami | host=web-01",
	}).Error; err != nil {
		t.Fatalf("seed audit: %v", err)
	}

	call := func(format string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/audit/export?format="+format, nil)
		c.Set("user", "op")
		c.Set("user_role", db.RoleAdmin)
		c.Set("user_id", uint(1))
		c.Set(middleware.TenantIDContextKey, uint(1))
		s.handleExportAuditChain(c)
		return w
	}

	// 1. ECS: one JSON document per line, with the ECS vocabulary and the product's fields namespaced.
	ecs := call("ecs")
	if ecs.Code != http.StatusOK {
		t.Fatalf("ecs = %d (%s)", ecs.Code, ecs.Body.String())
	}
	if ct := ecs.Header().Get("Content-Type"); !strings.Contains(ct, "ndjson") {
		t.Fatalf("ecs Content-Type = %q", ct)
	}
	if cd := ecs.Header().Get("Content-Disposition"); !strings.HasSuffix(cd, `.ecs.ndjson"`) {
		t.Fatalf("ecs filename = %q", cd)
	}
	if warning := ecs.Header().Get("Warning"); !strings.Contains(warning, "cannot verify") {
		t.Fatalf("ecs Warning = %q, want the projection caveat", warning)
	}
	lines := strings.Split(strings.TrimSpace(ecs.Body.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("ecs lines = %d, want one per row", len(lines))
	}
	var event map[string]interface{}
	if err := json.Unmarshal([]byte(lines[0]), &event); err != nil {
		t.Fatalf("ecs line is not JSON: %v (%s)", err, lines[0])
	}
	inner, _ := event["event"].(map[string]interface{})
	if inner["action"] != "task_create" || inner["outcome"] != "success" || inner["dataset"] != "forgec2.audit" {
		t.Fatalf("ecs event block = %v", inner)
	}
	if event["@timestamp"] == nil || event["@timestamp"] == "" {
		t.Fatalf("ecs line carries no @timestamp: %v", event)
	}
	user, _ := event["user"].(map[string]interface{})
	if user["name"] != "op" {
		t.Fatalf("ecs user.name = %v", user)
	}
	host, _ := event["host"].(map[string]interface{})
	if host["id"] != "a-1" {
		t.Fatalf("ecs host.id = %v", host)
	}
	// The hashes travel but are namespaced: a SIEM must not mistake them for something it can verify.
	product, _ := event["forgec2"].(map[string]interface{})
	if product["entry_hash"] == nil {
		t.Fatalf("ecs projection dropped the entry hash: %v", product)
	}

	// 2. CEF: one line per row, with the two escape grammars handled.
	cef := call("cef")
	if cef.Code != http.StatusOK {
		t.Fatalf("cef = %d (%s)", cef.Code, cef.Body.String())
	}
	if cd := cef.Header().Get("Content-Disposition"); !strings.HasSuffix(cd, `.cef"`) {
		t.Fatalf("cef filename = %q", cd)
	}
	cefLine := strings.TrimSpace(cef.Body.String())
	if !strings.HasPrefix(cefLine, "CEF:0|ForgeC2|") {
		t.Fatalf("cef line = %q", cefLine)
	}
	// The extension block must escape the characters that would truncate it in a collector: the resource holds a pipe and the
	// details hold an equals, and either unescaped shifts every later field.
	for _, want := range []string{`cs1=a\|b`, `cmd\=whoami`, `host\=web-01`, "suser=op", "outcome=success"} {
		if !strings.Contains(cefLine, want) {
			t.Fatalf("cef line is missing %q: %q", want, cefLine)
		}
	}
	if strings.Count(cefLine, "msg=") != 1 {
		t.Fatalf("cef line has more than one msg field: %q", cefLine)
	}
	// 3. The verifiable export is unchanged and carries no projection warning.
	ndjson := call("ndjson")
	if ndjson.Header().Get("Warning") != "" {
		t.Fatalf("the verifiable export grew a warning: %q", ndjson.Header().Get("Warning"))
	}
}
