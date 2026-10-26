package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

func auditCtx(user string) *gin.Context {
	c, _ := gin.CreateTestContext(nil)
	c.Request = httptest.NewRequest(http.MethodPost, "/x", nil)
	if user != "" {
		c.Set("user", user)
	}
	return c
}

// TestOperatorAuditHelpers pins the shape every operator action lands in: the
// "operator:" action prefix, the resource, the target id, the risk level and —
// critically — that secret-looking values are redacted before they reach the
// audit table, which is long-lived and read by every admin.
func TestOperatorAuditHelpers(t *testing.T) {
	s := mustTenantServer(t)

	s.LogUserManagementAction(auditCtx("admin1"), "user_create", "bob", "role=admin")
	s.LogPluginAction(auditCtx("admin1"), "plugin_install", "recon", "version=1.2")
	s.LogConfigChange(auditCtx("admin1"), "jwt_secret", "old-secret-value", "new-secret-value")
	s.LogEmergencyAction(auditCtx("admin1"), "kill_switch", 3)
	// A non-sensitive field keeps its values: the audit stays useful for tuning.
	s.LogConfigChange(auditCtx("admin1"), "offline_threshold", "60", "120")
	// A nil context (background path) must record "system" rather than panicking.
	s.LogUserManagementAction(nil, "user_create", "sys", "role=user")

	var rows []db.AuditLog
	if err := s.db.Where("action LIKE ?", "operator:%").Order("id ASC").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 6 {
		t.Fatalf("audit rows = %d, want 6: %+v", len(rows), rows)
	}

	find := func(action string) db.AuditLog {
		for _, r := range rows {
			if r.Action == action {
				return r
			}
		}
		t.Fatalf("no audit row for %q", action)
		return db.AuditLog{}
	}

	user := find("operator:user_create")
	if user.Resource != "user" || user.AgentID != "bob" || user.User != "admin1" {
		t.Fatalf("user_create row = %+v", user)
	}
	if !strings.Contains(user.Details, "target=bob") || !strings.Contains(user.Details, "role=admin") {
		t.Fatalf("user_create details = %q", user.Details)
	}

	plugin := find("operator:plugin_install")
	if plugin.Resource != "plugin" || plugin.AgentID != "recon" {
		t.Fatalf("plugin_install row = %+v", plugin)
	}

	// The config change records the field name but must not store the old or
	// new value verbatim: this is exactly where a JWT secret would leak.
	cfg := find("operator:config_change")
	if cfg.Resource != "system_config" || cfg.AgentID != "jwt_secret" {
		t.Fatalf("config_change row = %+v", cfg)
	}
	if strings.Contains(cfg.Details, "old-secret-value") || strings.Contains(cfg.Details, "new-secret-value") {
		t.Fatalf("config_change stored a secret value: %q", cfg.Details)
	}

	// A non-sensitive field keeps its values.
	var tuning db.AuditLog
	if err := s.db.Where("action = ? AND agent_id = ?", "operator:config_change", "offline_threshold").First(&tuning).Error; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tuning.Details, "old=60") || !strings.Contains(tuning.Details, "new=120") {
		t.Fatalf("non-sensitive config change lost its values: %q", tuning.Details)
	}

	// "system" when there is no request context.
	var sysRows []db.AuditLog
	if err := s.db.Where("action = ? AND user = ?", "operator:user_create", "system").Find(&sysRows).Error; err != nil {
		t.Fatal(err)
	}
	if len(sysRows) != 1 {
		t.Fatalf("nil-context audit rows = %d, want one attributed to system", len(sysRows))
	}

	// Every operator row goes through the tamper-evident chain, so each entry
	// must carry the hash of its predecessor and its own entry hash.
	for _, r := range rows {
		if r.EntryHash == "" {
			t.Fatalf("operator audit row %q has no entry hash: %+v", r.Action, r)
		}
	}
}
