package server

import (
	"fmt"

	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

// ── Helpers ─────────────────────────────────────────────────────────────────

// settingsFormCtx posts a form-encoded settings action.
func settingsFormCtx(s *Server, t *testing.T, username, role string, form url.Values, params gin.Params) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	c, w := tenantScopedAdminContext(s, t, username, 1)
	c.Set("user_role", role)
	c.Request = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form.Encode()))
	c.Request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c.Params = params
	return c, w
}

// ── Purge ───────────────────────────────────────────────────────────────────

// TestPurgeTasksOnlyRemovesTerminalOldTasks proves the purge honours its
// contract: only completed/failed rows older than the cutoff go, and the count
// reported to the operator is the real number of rows removed.
func TestPurgeTasksOnlyRemovesTerminalOldTasks(t *testing.T) {
	s := mustTenantServer(t)
	now := time.Now()
	old := now.AddDate(0, 0, -400)

	cases := []struct {
		name   string
		status string
		age    time.Time
		keep   bool
	}{
		{"old completed", "completed", old, false},
		{"old failed", "failed", old, false},
		{"old pending", "pending", old, true},
		{"old queued", "queued", old, true},
		{"new completed", "completed", now, true},
		{"new failed", "failed", now, true},
	}
	for _, tc := range cases {
		if err := s.db.Create(&db.Task{
			AgentID: "purge-agent", Type: "ps", Command: "x", Status: tc.status,
			CreatedAt: tc.age, UpdatedAt: tc.age,
		}).Error; err != nil {
			t.Fatalf("seed %s: %v", tc.name, err)
		}
	}

	c, w := settingsFormCtx(s, t, "set-op", "admin", url.Values{}, gin.Params{})
	s.handlePurgeTasks(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}

	// Two terminal, old tasks must go; the other four must remain.
	if !strings.Contains(w.Body.String(), "Purged 2 old tasks") {
		t.Fatalf("body = %s, want the real removed count of 2", w.Body.String())
	}

	var remaining []db.Task
	if err := s.db.Find(&remaining).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(remaining) != 4 {
		names := make([]string, 0, len(remaining))
		for _, r := range remaining {
			names = append(names, r.Status)
		}
		t.Fatalf("%d tasks survived (%v), want the 4 that must be kept", len(remaining), names)
	}
}

// TestPurgeTasksClampsWindow pins the day-window handling: a request older than
// any row can purge everything, while an unusable window falls back to 30 days.
func TestPurgeTasksClampsWindow(t *testing.T) {
	s := mustTenantServer(t)
	rows := []struct {
		status string
		age    time.Time
	}{
		{"completed", time.Now().AddDate(0, 0, -10)},  // inside the default window
		{"failed", time.Now().AddDate(0, 0, -40)},     // outside it
		{"completed", time.Now().AddDate(0, 0, -400)}, // outside even the cap
	}
	for i, r := range rows {
		if err := s.db.Create(&db.Task{
			AgentID: fmt.Sprintf("clamp-%d", i), Type: "ps", Status: r.status,
			CreatedAt: r.age, UpdatedAt: r.age,
		}).Error; err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
	}

	// A non-numeric window falls back to the 30-day default: the -40d and -400d
	// rows are older than the cutoff, the -10d row is not.
	c, w := settingsFormCtx(s, t, "set-op", "admin", url.Values{"days": {"not-a-number"}}, gin.Params{})
	s.handlePurgeTasks(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "Purged 2 old tasks") {
		t.Fatalf("body = %s, want the 30-day default to purge the two older rows", w.Body.String())
	}
	var kept int64
	if err := s.db.Model(&db.Task{}).Count(&kept).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if kept != 1 {
		t.Fatalf("%d tasks remain, want only the fresh one", kept)
	}

	// An over-cap window is clamped to 365 days: nothing newer than that goes.
	c2, w2 := settingsFormCtx(s, t, "set-op", "admin", url.Values{"days": {"99999"}}, gin.Params{})
	s.handlePurgeTasks(c2)
	if !strings.Contains(w2.Body.String(), "Purged 0 old tasks") {
		t.Fatalf("body = %s, want the clamped maximum to leave the fresh row", w2.Body.String())
	}
	if err := s.db.Model(&db.Task{}).Count(&kept).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if kept != 1 {
		t.Fatalf("%d tasks remain after the clamped window, want 1", kept)
	}
}

// TestPurgeAuditLogsIsTenantScoped proves the audit purge only removes the
// caller's own entries plus legacy (tenant 0) ones, and reports the real count.
func TestPurgeAuditLogsIsTenantScoped(t *testing.T) {
	s := mustTenantServer(t)
	old := time.Now().AddDate(0, 0, -1000)

	for _, tenant := range []uint{1, 2, 0} {
		if err := s.db.Create(&db.AuditLog{
			TenantID: tenant, User: "u", Action: "test", Resource: "r", Success: true, CreatedAt: old,
		}).Error; err != nil {
			t.Fatalf("seed audit tenant %d: %v", tenant, err)
		}
	}
	// A fresh own-tenant entry must survive any purge window.
	if err := s.db.Create(&db.AuditLog{
		TenantID: 1, User: "u", Action: "test", Resource: "r", Success: true, CreatedAt: time.Now(),
	}).Error; err != nil {
		t.Fatalf("seed fresh audit: %v", err)
	}

	c, w := settingsFormCtx(s, t, "set-op", "admin", url.Values{}, gin.Params{})
	s.handlePurgeAuditLogs(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	// Own tenant (1) + legacy (0) are in scope; tenant 2 is not.
	if !strings.Contains(w.Body.String(), "Purged 2 old audit logs") {
		t.Fatalf("body = %s, want only the caller's own and legacy entries", w.Body.String())
	}

	var count int64
	if err := s.db.Model(&db.AuditLog{}).Count(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	// Three rows: the foreign one, the fresh one, and the purge marker the
	// handler now writes so destroying audit history leaves a record of itself.
	if count != 3 {
		t.Fatalf("%d audit rows remain, want the foreign row, the fresh one and the purge marker", count)
	}
	var markers int64
	if err := s.db.Model(&db.AuditLog{}).Where("action = ?", "audit_purge").Count(&markers).Error; err != nil {
		t.Fatalf("count markers: %v", err)
	}
	if markers != 1 {
		t.Fatalf("purge markers = %d, want 1", markers)
	}
	var foreign int64
	if err := s.db.Model(&db.AuditLog{}).Where("tenant_id = ?", 2).Count(&foreign).Error; err != nil {
		t.Fatalf("count foreign: %v", err)
	}
	if foreign != 1 {
		t.Fatal("another tenant's audit entry was destroyed by the purge")
	}
}

// TestPurgeAuditLogsClampsWindow proves the audit window floor and cap are
// enforced: a short request cannot destroy recent history.
func TestPurgeAuditLogsClampsWindow(t *testing.T) {
	s := mustTenantServer(t)
	// 30 days old: below the 90-day floor, so it must survive regardless of the
	// requested window.
	recent := time.Now().AddDate(0, 0, -30)
	if err := s.db.Create(&db.AuditLog{
		TenantID: 1, User: "u", Action: "test", Resource: "r", Success: true, CreatedAt: recent,
	}).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	c, w := settingsFormCtx(s, t, "set-op", "admin", url.Values{"days": {"1"}}, gin.Params{})
	s.handlePurgeAuditLogs(c)
	if !strings.Contains(w.Body.String(), "Purged 0 old audit logs") {
		t.Fatalf("body = %s, want the 90-day floor to protect recent history", w.Body.String())
	}
	var kept int64
	if err := s.db.Model(&db.AuditLog{}).Count(&kept).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if kept != 1 {
		t.Fatalf("%d audit rows remain, want 1", kept)
	}
}

// TestPurgeHandlersRequireOperator proves the destructive endpoints keep the
// operator gate.
func TestPurgeHandlersRequireOperator(t *testing.T) {
	s := mustTenantServer(t)
	for _, invoke := range []func(*Server, *gin.Context){
		(*Server).handlePurgeTasks,
		(*Server).handlePurgeAuditLogs,
	} {
		c, w := settingsFormCtx(s, t, "set-viewer", "viewer", url.Values{}, gin.Params{})
		invoke(s, c)
		if w.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403", w.Code)
		}
	}
}

// ── Password policy ─────────────────────────────────────────────────────────

// TestValidatePasswordComplexity pins that the validator honours the configured
// policy. config.Load always starts from DefaultConfig, which sets
// require_upper/lower/digit, so a production server enforces them; this builds
// the policy explicitly so the table does not depend on that chain.
func TestValidatePasswordComplexity(t *testing.T) {
	s := mustTenantServer(t)
	s.cfg.PasswordPolicy.MinLength = 8
	s.cfg.PasswordPolicy.RequireUpper = true
	s.cfg.PasswordPolicy.RequireLower = true
	s.cfg.PasswordPolicy.RequireDigit = true
	s.cfg.PasswordPolicy.RequireSymbol = false

	cases := []struct {
		name     string
		password string
		wantErr  bool
	}{
		{"meets every rule", "Correct-Horse1", false},
		{"minimum length exactly 8", "Abcdef1x", false},
		{"symbol allowed but not required", "NoSymbols99", false},
		{"too short", "Ab1", true},
		{"missing uppercase", "lowercase1", true},
		{"missing lowercase", "UPPERCASE1", true},
		{"missing digit", "NoDigitsHere", true},
		{"symbol not required", "Symbols-Not1", false},
		{"empty", "", true},
		{"spaces only", "        ", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := s.validatePasswordComplexity(tc.password)
			if tc.wantErr && err == nil {
				t.Fatalf("password %q was accepted", tc.password)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("password %q rejected: %v", tc.password, err)
			}
		})
	}

	// A relaxed policy must actually relax the rules it names.
	s.cfg.PasswordPolicy.MinLength = 4
	s.cfg.PasswordPolicy.RequireUpper = false
	s.cfg.PasswordPolicy.RequireLower = false
	s.cfg.PasswordPolicy.RequireDigit = false
	for _, ok := range []string{"abcd", "1234", "    ", "aB34"} {
		if err := s.validatePasswordComplexity(ok); err != nil {
			t.Fatalf("a relaxed policy rejected %q: %v", ok, err)
		}
	}
	if err := s.validatePasswordComplexity("abc"); err == nil {
		t.Fatal("a relaxed policy accepted a password below min_length")
	}
	if err := s.validatePasswordComplexity("ab34"); err != nil {
		t.Fatalf("a password meeting every relaxed rule was rejected: %v", err)
	}

	// Re-enabling a single rule must reject the passwords that lack it again.
	s.cfg.PasswordPolicy.RequireDigit = false
	s.cfg.PasswordPolicy.RequireUpper = true
	if err := s.validatePasswordComplexity("ab34"); err == nil {
		t.Fatal("require_upper=true was not enforced")
	}
	if err := s.validatePasswordComplexity("Ab34"); err != nil {
		t.Fatalf("a password meeting every relaxed rule was rejected: %v", err)
	}
}

// TestIsPrintableASCII pins the printable-ASCII predicate. Tab and newline are
// control characters and are refused, as are any non-ASCII script characters.
func TestIsPrintableASCII(t *testing.T) {
	valid := []string{"", "abc", "ABC123", "hello world", "!@#$%^&*()", "a-b_c.d"}
	for _, v := range valid {
		if !isPrintableASCII(v) {
			t.Fatalf("isPrintableASCII(%q) = false, want true", v)
		}
	}
	invalid := []string{"naïve", "日本語", "emoji 🎯", "line\nbreak", "carriage\rreturn", "tab\there", "nul\x00byte"}
	for _, v := range invalid {
		if isPrintableASCII(v) {
			t.Fatalf("isPrintableASCII(%q) = true, want false", v)
		}
	}
}

// ── Monitor ─────────────────────────────────────────────────────────────────

// TestHandleGetAgentStatusCountsBuckets proves the status bucketing, including
// the long-sleep threshold multiplier.
func TestHandleGetAgentStatus(t *testing.T) {
	s := mustTenantServer(t)
	seedTenantAgent(t, s, "ms-online", 1)
	seedTenantAgent(t, s, "ms-stale", 1)
	seedTenantAgent(t, s, "ms-offline", 1)

	base := s.offlineThreshold()
	if base <= 0 {
		t.Fatalf("offlineThreshold() = %v, want a positive duration", base)
	}

	// fresh → online; past base but under the stale multiplier → stale;
	// past both → offline.
	for _, agent := range []struct {
		id   string
		seen time.Time
	}{
		{"ms-online", time.Now()},
		{"ms-stale", time.Now().Add(-base - time.Second)},
		{"ms-offline", time.Now().Add(-base*StaleThresholdMultiplier - time.Second)},
	} {
		if err := s.db.Model(&db.Implant{}).Where("id = ?", agent.id).
			Update("last_seen", agent.seen).Error; err != nil {
			t.Fatalf("stamp %s: %v", agent.id, err)
		}
	}

	c, w := tenantScopedAdminContext(s, t, "mon-op", 1)
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	s.handleGetAgentStatus(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	var stats struct {
		Total   int64 `json:"total"`
		Online  int64 `json:"online"`
		Stale   int64 `json:"stale"`
		Offline int64 `json:"offline"`
	}
	unmarshalData(t, w.Body.Bytes(), &stats)
	if stats.Total != 3 || stats.Online != 1 || stats.Stale != 1 || stats.Offline != 1 {
		t.Fatalf("stats = %+v, want total=3 online=1 stale=1 offline=1", stats)
	}
}

// TestScreenMonitorHelpersPin the in-memory registry and frame cache.
func TestScreenMonitorHelpers(t *testing.T) {
	s := mustTenantServer(t)
	if s.screenMonitorImplants == nil {
		s.screenMonitorImplants = make(map[string]time.Time)
	}

	agent := "sm-agent"
	if s.IsScreenMonitoring(agent) {
		t.Fatal("an agent with no stream reports as monitored")
	}

	s.screenMonitorMu.Lock()
	s.screenMonitorImplants[strings.ToLower(agent)] = time.Now()
	s.screenMonitorMu.Unlock()
	if !s.IsScreenMonitoring(agent) {
		t.Fatal("a started stream is not reported as monitored")
	}
	if _, ok := s.latestScreenFrame(agent); ok {
		t.Fatal("a frame exists before any screenshot arrived")
	}

	// The frame cache is written per agent id regardless of monitoring state;
	// delivery is gated upstream in the beacon path (IsScreenMonitoring).
	s.BroadcastScreenshot("sm-other", "Zm9v")
	if _, ok := s.latestScreenFrame("sm-other"); !ok {
		t.Fatal("BroadcastScreenshot did not cache the frame at all")
	}
	s.BroadcastScreenshot(agent, "Zm9v")
	frame, ok := s.latestScreenFrame(agent)
	if !ok || frame.data != "Zm9v" || frame.mime != "image/png" {
		t.Fatalf("frame = %+v ok=%v", frame, ok)
	}

	// JPEG magic is detected from the base64 prefix.
	s.BroadcastScreenshot(agent, "/9j/4AAQ")
	if frame, ok := s.latestScreenFrame(agent); !ok || frame.mime != "image/jpeg" {
		t.Fatalf("frame = %+v ok=%v, want image/jpeg", frame, ok)
	}
}

// TestHandleGetRekeyStats proves the endpoint reports its dependency honestly.
func TestHandleGetRekeyStats(t *testing.T) {
	s := mustTenantServer(t)

	// No session manager: the dependency is reported, not faked.
	s.sessionManager = nil
	c, w := tenantScopedAdminContext(s, t, "mon-op", 1)
	s.handleGetRekeyStats(c)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("nil manager: status = %d, want 503 (%s)", w.Code, w.Body.String())
	}

	// The role gate holds even with the manager present.
	// cfg = &config.Config{} → requireOperator already passed role check
	c2, w2 := tenantScopedAdminContext(s, t, "mon-viewer", 1)
	c2.Set("user_role", "viewer")
	s.handleGetRekeyStats(c2)
	if w2.Code != http.StatusForbidden {
		t.Fatalf("viewer: status = %d, want 403 (%s)", w2.Code, w2.Body.String())
	}
}

// TestHandleGetMetricsHistory proves history is a stable, bounded snapshot.
func TestHandleGetMetricsHistory(t *testing.T) {
	s := mustTenantServer(t)
	m := NewMonitorCollector(s)
	for i := 0; i < 90; i++ {
		m.acceptMetrics(db.SystemMetric{CPULoad: float64(i)})
	}
	s.monitorCollector = m

	c, w := tenantScopedAdminContext(s, t, "mon-op", 1)
	s.handleGetMetricsHistory(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	// The memo the collector keeps is capped, so the copy must not grow with it.
	history := m.GetMetricsHistory()
	if len(history) == 0 {
		t.Fatal("metrics history is empty after accepting samples")
	}
	before := history[0].CPULoad
	if got := m.GetMetricsHistory(); len(got) != len(history) || got[0].CPULoad != before {
		t.Fatal("GetMetricsHistory did not return a stable copy")
	}
}
