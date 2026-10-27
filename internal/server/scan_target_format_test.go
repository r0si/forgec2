package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	forgecrypto "github.com/forgec2/forgec2/internal/crypto"
	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

func scanCtx(t *testing.T, form url.Values) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/scan", strings.NewReader(form.Encode()))
	c.Request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c.Set("user", "admin1")
	c.Set("user_role", db.RoleAdmin)
	c.Set("user_id", uint(1))
	return c, w
}

// TestHandleScanTaskRejectsColonTarget pins the fix for an IPv6 target: the wire
// format is "<target>:<ports>" and the implant splits on the FIRST colon, so
// "2001:db8::1" became target "2001" with a nonsense port list — the scan hit the
// wrong host.
func TestHandleScanTaskRejectsColonTarget(t *testing.T) {
	s := mustTenantServer(t)
	forgecrypto.InitLootEncryption(testStorageKeyHex)
	const agentID = "99999999-9999-9999-9999-999999999999"
	seedIOCAgent(t, s, agentID)

	for _, target := range []string{"2001:db8::1", "10.0.0.1:80", "10.0.0.1 10.0.0.2", "10.0.0.1\n"} {
		c, w := scanCtx(t, url.Values{
			"agent_id": {agentID}, "target": {target}, "port_range": {"80,443"},
		})
		s.handleScanTask(c)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("target %q = %d, want 400 (%s)", target, w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), "first colon") {
			t.Fatalf("the refusal does not explain the format: %s", w.Body.String())
		}
	}

	// No task was queued for any refusal.
	var count int64
	s.db.Model(&db.Task{}).Where("agent_id = ? AND type = ?", agentID, "portscan").Count(&count)
	if count != 0 {
		t.Fatalf("%d scan tasks were queued despite the refusals", count)
	}
}

// TestHandleScanTaskBuildsCommaSeparatedTargets pins the supported shape: a
// comma-separated target list still builds "<target>:<ports>".
func TestHandleScanTaskBuildsCommaSeparatedTargets(t *testing.T) {
	s := mustTenantServer(t)
	forgecrypto.InitLootEncryption(testStorageKeyHex)
	const agentID = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	seedIOCAgent(t, s, agentID)

	c, w := scanCtx(t, url.Values{
		"agent_id": {agentID}, "target": {"10.0.0.1,10.0.0.2"}, "port_range": {"80,443"},
	})
	s.handleScanTask(c)
	if w.Code != http.StatusOK && w.Code != http.StatusCreated {
		t.Fatalf("status = %d (%s)", w.Code, w.Body.String())
	}

	var task db.Task
	if err := s.db.Where("agent_id = ? AND type = ?", agentID, "portscan").First(&task).Error; err != nil {
		t.Fatalf("no scan task was queued: %v", err)
	}
	if !strings.HasPrefix(task.Command, "10.0.0.1,10.0.0.2:") {
		t.Fatalf("command = %q", task.Command)
	}
}

// TestValidateScanTarget pins the guard directly, including the shapes that must
// still pass: a bare IPv4 and a comma-separated multi-target list.
func TestValidateScanTarget(t *testing.T) {
	for _, bad := range []string{
		"2001:db8::1",       // IPv6 literal: the implant would read target "2001"
		"10.0.0.1:80",       // host:port already carries a colon
		"10.0.0.1 10.0.0.2", // space-separated list
		"10.0.0.1\n",        // trailing newline
		"10.0.0.1\t",
		"",
	} {
		if err := validateScanTarget(bad); err == nil {
			t.Errorf("validateScanTarget(%q) = nil, want an error", bad)
		}
	}
	for _, good := range []string{"10.0.0.1", "10.0.0.1,10.0.0.2", "dc01.corp.local", "10.0.0.1,dc01.corp.local"} {
		if err := validateScanTarget(good); err != nil {
			t.Errorf("validateScanTarget(%q) = %v, want nil", good, err)
		}
	}
}
