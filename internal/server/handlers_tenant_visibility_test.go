package server

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/config"
	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/testutil"
	"github.com/forgec2/forgec2/pkg/encoding"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"gorm.io/gorm"
)

const tenantVisibilityMasterHex = "aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899"

// tenantScopedAdminContext builds a gin context authenticated as a user with
// the given tenant id, mirroring the real auth middleware contract.
func tenantScopedAdminContext(s *Server, t *testing.T, username string, tenantID uint) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	var user db.User
	if err := s.db.Where("username = ?", username).First(&user).Error; err == gorm.ErrRecordNotFound {
		user = db.User{Username: username, TenantID: tenantID, Role: "admin", IsActive: true}
		if err := s.db.Create(&user).Error; err != nil {
			t.Fatalf("seed user: %v", err)
		}
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest(http.MethodGet, "/", nil)
	c.Request.Header.Set("Accept", "application/json")
	c.Set("user", username)
	c.Set("user_role", "admin")
	return c, w
}

// TestTenantScopeFailClosedOnLookupError proves a broken tenant lookup
// (authenticated identity, DB error rather than a missing row) denies
// everything instead of degrading to legacy global visibility.
func TestTenantScopeFailClosedOnLookupError(t *testing.T) {
	ginSetTestMode(t)
	database := testutil.SetupTestDB(t)
	s := &Server{db: database}
	sqlDB, err := database.DB()
	if err != nil {
		t.Fatalf("sql db: %v", err)
	}
	sqlDB.Close()

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest(http.MethodGet, "/", nil)
	c.Set("user", "someone")

	if _, ok := s.lookupTenantID(c); ok {
		t.Fatal("lookupTenantID on broken DB: ok=true, want false")
	}
}

// TestTenantScopeLegacyUnscopedUnchanged proves contexts without identity
// (tests, system paths) keep legacy global visibility.
func TestTenantScopeLegacyUnscopedUnchanged(t *testing.T) {
	ginSetTestMode(t)
	s := initV3BeaconServer(t, testutil.SetupTestDB(t), tenantVisibilityMasterHex)
	if err := s.db.Create(&db.Implant{ID: "t-leg", TenantID: 9}).Error; err != nil {
		t.Fatalf("seed agent: %v", err)
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest(http.MethodGet, "/", nil)

	var count int64
	if err := s.tenantScope(s.db.Model(&db.Implant{}), c).Count(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("legacy context saw %d rows, want 1", count)
	}
}

// TestShellPageCrossTenant404 proves the shell page honors tenant scope
// (previously an unscoped read: direct IDOR on other tenants' agents).
func TestShellPageCrossTenant404(t *testing.T) {
	ginSetTestMode(t)
	s := initV3BeaconServer(t, testutil.SetupTestDB(t), tenantVisibilityMasterHex)
	if err := s.db.Create(&db.Implant{ID: "t-shell", TenantID: 2, Hostname: "OTHER"}).Error; err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	c, w := tenantScopedAdminContext(s, t, "shell-viewer", 1)
	c.Params = gin.Params{{Key: "id", Value: "t-shell"}}

	s.handleShellPage(c)

	if w.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant shell page status=%d, want 404", w.Code)
	}
}

// TestResolveVisibleAgentIDScopesHostname proves hostname resolution (the
// AI-assist path) stays inside the caller's tenant.
func TestResolveVisibleAgentIDScopesHostname(t *testing.T) {
	ginSetTestMode(t)
	s := initV3BeaconServer(t, testutil.SetupTestDB(t), tenantVisibilityMasterHex)
	if err := s.db.Create(&db.Implant{ID: "t-vis", TenantID: 2, Hostname: "SHARED-NAME"}).Error; err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	c, _ := tenantScopedAdminContext(s, t, "vis-viewer", 1)

	if got := s.resolveVisibleAgentID(c, "SHARED-NAME"); got != "" {
		t.Fatalf("resolved cross-tenant agent %q by hostname", got)
	}
	if got := s.resolveVisibleAgentID(c, "t-vis"); got != "" {
		t.Fatalf("resolved cross-tenant agent %q by id", got)
	}
}

// TestV3RegistrationAssignsDefaultTenant guards the root-cause bug: a fresh
// v3 registration creates its implant row via ensureBeaconImplantRow, which
// must NOT leave the row tenant-less (tenant_id=0) — otherwise tenantScope
// hides the brand-new agent from every operator.
func TestV3RegistrationAssignsDefaultTenant(t *testing.T) {
	ginSetTestMode(t)
	database := testutil.SetupTestDB(t)
	s := initV3BeaconServer(t, database, tenantVisibilityMasterHex)

	id, secretB64, err := s.createRegSecret(0)
	if err != nil {
		t.Fatalf("createRegSecret: %v", err)
	}
	secret, err := base64.StdEncoding.DecodeString(secretB64)
	if err != nil || len(secret) != 32 {
		t.Fatalf("v3 secret invalid: err=%v len=%d", err, len(secret))
	}

	conn, done := tcpFrameConn(t, s)
	defer done()
	agent := newTCPTestAgent(t, "aaaaaaaa-bbbb-4333-8444-cccccccccccc").
		withRawRegKey(secret).
		withSecretID(id)
	tcpWriteFrame(t, conn, []byte(agent.registerFrame()))
	respFrame := tcpReadFrame(t, conn)
	var regResp struct {
		RegOK bool `json:"reg_ok"`
	}
	if err := encoding.Unmarshal(respFrame, &regResp); err != nil || !regResp.RegOK {
		t.Fatalf("v3 registration failed: %v (body=%s)", err, respFrame)
	}

	var implant db.Implant
	if err := database.Where("id = ?", agent.uuid).First(&implant).Error; err != nil {
		t.Fatalf("agent row missing after registration: %v", err)
	}
	want := s.defaultTenantID()
	if want == 0 {
		t.Fatal("default tenant missing from test DB")
	}
	if implant.TenantID != want {
		t.Fatalf("fresh v3 implant tenant_id=%d, want %d (default tenant); row is invisible to tenant-scoped operators",
			implant.TenantID, want)
	}
}

// TestBeaconRegistrationTenant verifies the encrypted-beacon path:
//  1. a brand-new agent is created in the default tenant;
//  2. a legacy tenant_id=0 row (pre-fix placeholder) is self-healed on its
//     next beacon so tenantScope no longer hides it.
func TestBeaconRegistrationTenant(t *testing.T) {
	ginSetTestMode(t)
	database := testutil.SetupTestDB(t)
	s := initV3BeaconServer(t, database, tenantVisibilityMasterHex)
	if s.defaultTenantID() == 0 {
		t.Fatal("default tenant missing from test DB")
	}

	beacon := func(t *testing.T, uuid string) db.Implant {
		t.Helper()
		req := beaconRequest{
			UUID:            uuid,
			ProtocolVersion: 3,
			Info: map[string]string{
				"hostname": "WORKSTATION01",
				"username": "alice",
				"os":       "windows",
				"arch":     "amd64",
				"ip":       "10.0.0.5",
			},
		}
		agent, _ := s.processAgentRegistration(req, "203.0.113.7", time.Now())
		var row db.Implant
		if err := database.Where("id = ?", agent.ID).First(&row).Error; err != nil {
			t.Fatalf("implant row missing after beacon: %v", err)
		}
		return row
	}

	t.Run("new agent gets default tenant", func(t *testing.T) {
		row := beacon(t, "new-0000-0000-0000-000000000001")
		if row.TenantID != s.defaultTenantID() {
			t.Fatalf("new agent tenant_id=%d, want %d", row.TenantID, s.defaultTenantID())
		}
	})

	t.Run("legacy tenant0 row self-heals", func(t *testing.T) {
		legacy := "legacy-0000-0000-0000-000000000002"
		// Reproduce the pre-fix placeholder: row created by ensureBeaconImplantRow
		// without a tenant id (zero value) while the server was running.
		if err := database.Create(&db.Implant{
			ID: legacy, TenantID: 0, Hostname: "OLD", IP: "10.0.0.9",
			LastSeen: time.Now().Add(-time.Minute), Status: "online",
		}).Error; err != nil {
			t.Fatalf("seed legacy row: %v", err)
		}
		row := beacon(t, legacy)
		if row.TenantID != s.defaultTenantID() {
			t.Fatalf("legacy tenant0 row not self-healed: tenant_id=%d, want %d", row.TenantID, s.defaultTenantID())
		}
	})
}

// TestListAgentsTenantScoped guards the user-visible symptom: a tenant-scoped
// operator must only see agents owned by their tenant, and never tenant-less
// (0) placeholders.
func TestListAgentsTenantScoped(t *testing.T) {
	ginSetTestMode(t)
	cfg := &config.Config{}
	cfg.Server.OfflineThreshold = 60
	s := &Server{db: testutil.SetupTestDB(t), cfg: cfg, wsClients: make(map[*websocket.Conn]*wsClientConn)}

	if err := s.db.Create(&db.Implant{ID: "a1", TenantID: 1, Hostname: "DC01", IP: "10.0.0.1", LastSeen: time.Now()}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Create(&db.Implant{ID: "a2", TenantID: 2, Hostname: "WEB01", IP: "10.0.0.2", LastSeen: time.Now()}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Create(&db.Implant{ID: "a3", TenantID: 0, Hostname: "LEGACY", IP: "10.0.0.3", LastSeen: time.Now()}).Error; err != nil {
		t.Fatal(err)
	}

	c, w := tenantScopedAdminContext(s, t, "admin", 1)
	c.Request, _ = http.NewRequest(http.MethodGet, "/api/agents?page=1&page_size=10", nil)
	c.Request.Header.Set("Accept", "application/json")
	s.handleListAgents(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d body=%s", w.Code, w.Body.String())
	}
	var m map[string]any
	unmarshalData(t, w.Body.Bytes(), &m)
	agents, ok := m["agents"].([]any)
	if !ok {
		t.Fatalf("missing agents[]: %v", m)
	}
	if len(agents) != 1 {
		t.Fatalf("admin must see exactly their tenant's agent, got %d: %v", len(agents), agents)
	}
	if first := agents[0].(map[string]any); first["id"] != "a1" {
		t.Fatalf("expected only a1, got %v", first["id"])
	}
}

func TestLegacyAPIRoutesAreTenantScoped(t *testing.T) {
	ginSetTestMode(t)
	s := &Server{db: testutil.SetupTestDB(t), cfg: &config.Config{}, wsClients: make(map[*websocket.Conn]*wsClientConn)}
	for _, agent := range []db.Implant{
		{ID: "api-a1", TenantID: 1, Hostname: "TENANT-ONE"},
		{ID: "api-a2", TenantID: 2, Hostname: "TENANT-TWO"},
	} {
		if err := s.db.Create(&agent).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, task := range []db.Task{
		{AgentID: "api-a1", TenantID: 1, Type: "shell", Status: "completed", Result: "one"},
		{AgentID: "api-a2", TenantID: 2, Type: "shell", Status: "completed", Result: "two"},
	} {
		if err := s.db.Create(&task).Error; err != nil {
			t.Fatal(err)
		}
	}

	t.Run("agent detail hides another tenant", func(t *testing.T) {
		c, w := tenantScopedAdminContext(s, t, "api-admin-agent", 1)
		c.Params = gin.Params{{Key: "id", Value: "api-a2"}}
		c.Request, _ = http.NewRequest(http.MethodGet, "/api/v1/agents/api-a2", nil)
		s.apiGetAgent(c)
		if w.Code != http.StatusNotFound {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
	})

	t.Run("task list only returns current tenant", func(t *testing.T) {
		c, w := tenantScopedAdminContext(s, t, "api-admin-tasks", 1)
		c.Request, _ = http.NewRequest(http.MethodGet, "/api/v1/tasks", nil)
		s.apiListTasks(c)
		if w.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
		var response struct {
			Data []db.Task `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if len(response.Data) != 1 || response.Data[0].TenantID != 1 {
			t.Fatalf("unexpected tasks: %+v", response.Data)
		}
	})

	t.Run("delete cannot target another tenant", func(t *testing.T) {
		c, w := tenantScopedAdminContext(s, t, "api-admin-delete", 1)
		c.Params = gin.Params{{Key: "id", Value: "api-a2"}}
		c.Request, _ = http.NewRequest(http.MethodDelete, "/api/v1/agents/api-a2", nil)
		s.apiDeleteAgent(c)
		if w.Code != http.StatusNotFound {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
		var count int64
		if err := s.db.Model(&db.Implant{}).Where("id = ?", "api-a2").Count(&count).Error; err != nil || count != 1 {
			t.Fatalf("other tenant agent was deleted: count=%d err=%v", count, err)
		}
	})
}

// TestDashboardCountsTenantScoped verifies the dashboard totals and nav badge
// stats are filtered by the caller's tenant instead of counting the whole fleet.
func TestDashboardCountsTenantScoped(t *testing.T) {
	ginSetTestMode(t)
	cfg := &config.Config{}
	cfg.Server.OfflineThreshold = 60
	s := &Server{db: testutil.SetupTestDB(t), cfg: cfg, wsClients: make(map[*websocket.Conn]*wsClientConn)}

	seed := []db.Implant{
		{ID: "a1", TenantID: 1, Hostname: "DC01", IP: "10.0.0.1", LastSeen: time.Now()},
		{ID: "a2", TenantID: 2, Hostname: "WEB01", IP: "10.0.0.2", LastSeen: time.Now()},
	}
	for _, a := range seed {
		if err := s.db.Create(&a).Error; err != nil {
			t.Fatal(err)
		}
	}
	tasks := []db.Task{
		{AgentID: "a1", TenantID: 1, Type: "shell", Command: "whoami", Status: "pending", CreatedAt: time.Now()},
		{AgentID: "a2", TenantID: 2, Type: "shell", Command: "whoami", Status: "pending", CreatedAt: time.Now()},
	}
	for _, tk := range tasks {
		if err := s.db.Create(&tk).Error; err != nil {
			t.Fatal(err)
		}
	}

	c, w := tenantScopedAdminContext(s, t, "admin", 1)
	s.handleDashboard(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d body=%s", w.Code, w.Body.String())
	}
	var m map[string]any
	unmarshalData(t, w.Body.Bytes(), &m)
	checks := map[string]float64{
		"total_agents":  1,
		"online_agents": 1,
		"total_tasks":   1,
		"pending_tasks": 1,
		"today_tasks":   1,
	}
	for key, want := range checks {
		got, ok := m[key].(float64)
		if !ok || got != want {
			t.Fatalf("dashboard %s = %v (ok=%v), want %v (tenant-scoped)", key, m[key], ok, want)
		}
	}

	// Nav badge stats must be tenant-scoped too.
	stats := s.getNavStats(c)
	if got, ok := stats["online_count"].(int64); !ok || got != 1 {
		t.Fatalf("nav online_count = %v, want 1 (tenant-scoped)", stats["online_count"])
	}
	if got, ok := stats["pending_count"].(int64); !ok || got != 1 {
		t.Fatalf("nav pending_count = %v, want 1 (tenant-scoped)", stats["pending_count"])
	}
}

// TestAPIDashboardStatsUsesOwnershipAwareScopes guards the JSON dashboard
// endpoint against applying a tenant_id predicate to tables that do not own
// that column. Those rows are scoped through their owning agent/operator.
func TestAPIDashboardStatsUsesOwnershipAwareScopes(t *testing.T) {
	ginSetTestMode(t)
	cfg := &config.Config{}
	cfg.Server.OfflineThreshold = 60
	s := &Server{db: testutil.SetupTestDB(t), cfg: cfg, wsClients: make(map[*websocket.Conn]*wsClientConn)}

	c, w := tenantScopedAdminContext(s, t, "dashboard-admin", 1)
	if err := s.db.Create(&db.User{Username: "other-admin", TenantID: 2, Role: "admin", IsActive: true}).Error; err != nil {
		t.Fatal(err)
	}
	agents := []db.Implant{
		{ID: "dashboard-a1", TenantID: 1, Hostname: "ONE", LastSeen: time.Now()},
		{ID: "dashboard-a2", TenantID: 2, Hostname: "TWO", LastSeen: time.Now()},
	}
	for i := range agents {
		if err := s.db.Create(&agents[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	tasks := []db.Task{
		{AgentID: "dashboard-a1", TenantID: 1, Type: "shell", Status: "completed", CreatedAt: time.Now()},
		{AgentID: "dashboard-a2", TenantID: 2, Type: "shell", Status: "failed", CreatedAt: time.Now()},
	}
	for i := range tasks {
		if err := s.db.Create(&tasks[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, row := range []any{
		&db.CredentialEntry{AgentID: "dashboard-a1", Username: "one"},
		&db.CredentialEntry{AgentID: "dashboard-a2", Username: "two"},
		&db.TokenEntry{AgentID: "dashboard-a1", Username: "one"},
		&db.TokenEntry{AgentID: "dashboard-a2", Username: "two"},
		&db.AuditLog{User: "dashboard-admin", Action: "view", Resource: "dashboard", TenantID: 1, Success: true},
		&db.AuditLog{User: "other-admin", Action: "view", Resource: "dashboard", TenantID: 2, Success: true},
		&db.Listener{Name: "global-http", Scheme: "http", Host: "127.0.0.1", Port: 8080},
	} {
		if err := s.db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}

	s.apiDashboardStats(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var response struct {
		Success bool `json:"success"`
		Data    struct {
			TotalAgents    int64     `json:"total_agents"`
			TotalTasks     int64     `json:"total_tasks"`
			TotalCreds     int64     `json:"total_creds"`
			TotalTokens    int64     `json:"total_tokens"`
			TotalListeners int64     `json:"total_listeners"`
			TotalAudits    int64     `json:"total_audits"`
			RecentTasks    []db.Task `json:"recent_tasks"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if !response.Success {
		t.Fatalf("success=false body=%s", w.Body.String())
	}
	if response.Data.TotalAgents != 1 || response.Data.TotalTasks != 1 ||
		response.Data.TotalCreds != 1 || response.Data.TotalTokens != 1 ||
		response.Data.TotalListeners != 1 || response.Data.TotalAudits != 1 {
		t.Fatalf("unexpected tenant-scoped dashboard counts: %+v", response.Data)
	}
	if len(response.Data.RecentTasks) != 1 || response.Data.RecentTasks[0].TenantID != 1 {
		t.Fatalf("recent tasks leaked another tenant: %+v", response.Data.RecentTasks)
	}
}
