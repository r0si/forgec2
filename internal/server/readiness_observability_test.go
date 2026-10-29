package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/plugin"
	"github.com/forgec2/forgec2/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// TestReadyReportsDeliveryAndActionHealth covers the two operational checks added to the readiness probe.
//
// The property under test is the one that makes a warning worth having: it fires when a route has NEVER delivered and a
// rule's actions have NEVER landed, and stays quiet otherwise. A probe that warns on one failure after a hundred successes
// is a probe an operator mutes, and a muted probe is worse than none.
func TestReadyReportsDeliveryAndActionHealth(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)
	resetNotificationStats()
	resetRuleActionStats()
	t.Cleanup(func() {
		resetNotificationStats()
		resetRuleActionStats()
	})

	ready := func() map[string]interface{} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/ready", nil)
		c.Set("user", "op")
		c.Set("user_role", db.RoleAdmin)
		c.Set("user_id", uint(1))
		c.Set(middleware.TenantIDContextKey, uint(1))
		s.handleReadyCheck(c)
		var resp map[string]interface{}
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
		return resp
	}

	// 1. Nothing recorded: both checks are present and quiet.
	payload := ready()
	checks, _ := payload["checks"].(map[string]interface{})
	if checks["notification_delivery"] == nil || checks["automation_actions"] == nil {
		t.Fatalf("the readiness checks do not include delivery and action health: %v", checks)
	}
	if checks["bundle_signing"] == nil {
		t.Fatalf("the readiness checks do not report the bundle signing state: %v", checks)
	}
	if checks["auto_retries"] == nil {
		t.Fatalf("the readiness checks do not include the auto-retry count: %v", checks)
	}
	if checks["plugins"] == nil {
		t.Fatalf("the readiness checks do not include the plugin count: %v", checks)
	}
	if payload["warnings"] != nil {
		t.Fatalf("a healthy server reported warnings: %v", payload["warnings"])
	}

	// 1b. A rejected plugin package is a warning: it is silently absent from the console otherwise.
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad")
	if err := os.MkdirAll(bad, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(bad, "manifest.yaml"), []byte("name: bad\n\tversion: 1.0.0\n"), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	s.pluginManager = plugin.NewManager(s.db)
	if err := s.pluginManager.LoadFromDisk(dir); err != nil {
		t.Fatalf("load plugins: %v", err)
	}
	payload = ready()
	warningsAfterPlugin, _ := payload["warnings"].([]interface{})
	if len(warningsAfterPlugin) != 1 || !containsFold(fmt.Sprint(warningsAfterPlugin[0]), "rejected") {
		t.Fatalf("a rejected package did not raise a warning: %v", payload["warnings"])
	}
	checks, _ = payload["checks"].(map[string]interface{})
	if got, _ := checks["plugins"].(string); !strings.Contains(got, "1 rejected") {
		t.Fatalf("the plugin check does not report the rejection: %q", got)
	}
	s.pluginManager = nil

	// 1b2. Bundle signing is reported and never warns: an unsigned bundle is a legitimate choice.
	signing, _ := checks["bundle_signing"].(string)
	if !strings.Contains(signing, "unsigned") && !strings.Contains(signing, "signed") {
		t.Fatalf("the signing state is unreadable: %q", signing)
	}
	if payload := ready(); payload["warnings"] != nil {
		t.Fatalf("the signing state raised a warning: %v", payload["warnings"])
	}

	// 1c. A BURST of automatic retries is a link-health warning; one retry is the feature working, not an alarm.
	resetAutoRetryCounter()
	recordAutoRetry()
	if payload := ready(); payload["warnings"] != nil {
		t.Fatalf("a single retry raised a warning: %v", payload["warnings"])
	}
	for i := 1; i < autoRetryReadyWarningThreshold; i++ {
		recordAutoRetry()
	}
	payload = ready()
	warningsAfterRetries, _ := payload["warnings"].([]interface{})
	foundRetryWarning := false
	for _, raw := range warningsAfterRetries {
		if strings.Contains(strings.ToLower(fmt.Sprint(raw)), "re-issued") {
			foundRetryWarning = true
		}
	}
	if !foundRetryWarning {
		t.Fatalf("a burst of retries did not raise a warning: %v", payload["warnings"])
	}
	resetAutoRetryCounter()

	// 2. A route that has only failed, and a rule whose actions have only failed: warnings.
	recordNotificationDelivery(11, errors.New("post: 500"))
	recordRuleActionOutcome("rule-x", false, errors.New("run_command: no target agent"))
	payload = ready()
	warnings, _ := payload["warnings"].([]interface{})
	if len(warnings) != 2 {
		t.Fatalf("warnings = %v, want one for the route and one for the rule", warnings)
	}
	checks, _ = payload["checks"].(map[string]interface{})
	if got, _ := checks["notification_delivery"].(string); got == "" || got == "ok (1 route(s) with recorded deliveries)" {
		t.Fatalf("the delivery check did not report the failure: %q", got)
	}

	// 3. One failure among successes is a blip, not a warning.
	resetNotificationStats()
	resetRuleActionStats()
	for i := 0; i < 5; i++ {
		recordNotificationDelivery(11, nil)
		recordRuleActionOutcome("rule-x", true, nil)
	}
	recordNotificationDelivery(11, errors.New("post: 500"))
	recordRuleActionOutcome("rule-x", false, errors.New("run_command: no target agent"))
	if payload := ready(); payload["warnings"] != nil {
		t.Fatalf("a blip raised warnings: %v", payload["warnings"])
	}

	// 4. It stays READY: a failing route is an alarm about notifications, not about the process.
	if status, _ := payload["status"].(string); status != "ok" {
		t.Fatalf("a failing route made the server unready: %q", status)
	}
}
