package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/forgec2/forgec2/internal/config"
	"github.com/forgec2/forgec2/internal/crypto"
	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

func newCredentialsTestServer(t *testing.T) *Server {
	t.Helper()
	gin.SetMode(gin.TestMode)
	crypto.InitLootEncryption(testStorageKeyHex)
	return &Server{db: newContractDB(t), cfg: config.DefaultConfig()}
}

func TestHandleListCredentials_Empty(t *testing.T) {
	s := newCredentialsTestServer(t)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user_role", db.RoleAdmin)
	c.Request, _ = http.NewRequest(http.MethodGet, "/api/credentials", nil)

	s.apiListCredentials(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Success bool                 `json:"success"`
		Data    []db.CredentialEntry `json:"data"`
		Total   int                  `json:"total"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid json: %v; body=%s", err, w.Body.String())
	}
	if !resp.Success {
		t.Fatal("expected success=true")
	}
	if resp.Total != 0 {
		t.Fatalf("expected total=0, got %d", resp.Total)
	}
	if len(resp.Data) != 0 {
		t.Fatalf("expected empty data, got %d entries", len(resp.Data))
	}
}

func TestHandleListCredentials_WithData(t *testing.T) {
	s := newCredentialsTestServer(t)

	entries := []db.CredentialEntry{
		{AgentID: "agent-1", Domain: "CORP", Username: "admin", Password: "P@ssw0rd", Type: "cleartext", Source: "manual"},
		{AgentID: "agent-2", Domain: "CORP", Username: "john", Hash: "aad3b435b51404eeaad3b435b51404ee", Type: "ntlm", Source: "mimikatz"},
	}
	if err := s.db.Create(&entries).Error; err != nil {
		t.Fatalf("seed credentials: %v", err)
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user_role", db.RoleAdmin)
	c.Request, _ = http.NewRequest(http.MethodGet, "/api/credentials", nil)

	s.apiListCredentials(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Success bool                 `json:"success"`
		Data    []db.CredentialEntry `json:"data"`
		Total   int                  `json:"total"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid json: %v; body=%s", err, w.Body.String())
	}
	if !resp.Success {
		t.Fatal("expected success=true")
	}
	if resp.Total != 2 {
		t.Fatalf("expected total=2, got %d", resp.Total)
	}
	if len(resp.Data) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(resp.Data))
	}

	foundAdmin := false
	for _, e := range resp.Data {
		if e.Username == "admin" && e.Domain == "CORP" && e.Type == "cleartext" {
			foundAdmin = true
		}
	}
	if !foundAdmin {
		t.Fatal("expected to find admin credential in response")
	}
}

func TestHandleCreateCredential_Success(t *testing.T) {
	s := newCredentialsTestServer(t)

	form := url.Values{}
	form.Set("type", "cleartext")
	form.Set("username", "svc_account")
	form.Set("password", "hunter2")
	form.Set("domain", "LAB")
	form.Set("notes", "service account")

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user_role", db.RoleAdmin)
	c.Request, _ = http.NewRequest(http.MethodPost, "/credentials/add", strings.NewReader(form.Encode()))
	c.Request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	s.handleAddCredential(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		ID uint `json:"id"`
	}
	unmarshalData(t, w.Body.Bytes(), &resp)
	if resp.ID == 0 {
		t.Fatal("expected non-zero credential ID")
	}

	var saved db.CredentialEntry
	if err := s.db.First(&saved, resp.ID).Error; err != nil {
		t.Fatalf("credential not found in DB: %v", err)
	}
	if saved.Username != "svc_account" {
		t.Fatalf("username = %q, want %q", saved.Username, "svc_account")
	}
	if saved.Domain != "LAB" {
		t.Fatalf("domain = %q, want %q", saved.Domain, "LAB")
	}
	if saved.Type != "cleartext" {
		t.Fatalf("type = %q, want %q", saved.Type, "cleartext")
	}
	if saved.Source != "manual" {
		t.Fatalf("source = %q, want %q", saved.Source, "manual")
	}
}

func TestHandleCreateCredential_MissingFields(t *testing.T) {
	s := newCredentialsTestServer(t)

	form := url.Values{}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user_role", db.RoleAdmin)
	c.Request, _ = http.NewRequest(http.MethodPost, "/credentials/add", strings.NewReader(form.Encode()))
	c.Request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	s.handleAddCredential(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Success bool `json:"success"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid json: %v; body=%s", err, w.Body.String())
	}
	if !resp.Success {
		t.Fatal("expected success=true (handler accepts empty fields)")
	}
}

func TestHandleDeleteCredential_Success(t *testing.T) {
	s := newCredentialsTestServer(t)

	cred := db.CredentialEntry{
		AgentID:  "agent-del",
		Username: "to_delete",
		Type:     "cleartext",
		Source:   "manual",
	}
	if err := s.db.Create(&cred).Error; err != nil {
		t.Fatalf("seed credential: %v", err)
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user_role", db.RoleAdmin)
	c.Request, _ = http.NewRequest(http.MethodDelete, "/credentials/1", nil)
	c.Params = gin.Params{{Key: "cred_id", Value: "1"}}

	s.handleDeleteCredential(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Success bool `json:"success"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid json: %v; body=%s", err, w.Body.String())
	}
	if !resp.Success {
		t.Fatal("expected success=true")
	}

	var count int64
	s.db.Model(&db.CredentialEntry{}).Count(&count)
	if count != 0 {
		t.Fatalf("expected 0 credentials after delete, got %d", count)
	}
}

func TestHandleDeleteCredential_NotFound(t *testing.T) {
	s := newCredentialsTestServer(t)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user_role", db.RoleAdmin)
	c.Request, _ = http.NewRequest(http.MethodDelete, "/credentials/99999", nil)
	c.Params = gin.Params{{Key: "cred_id", Value: "99999"}}

	s.handleDeleteCredential(c)

	// Tenant-gated pre-check: deleting a row that is not visible to the
	// caller (missing or foreign tenant) is 404, not silent 200.
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d; body=%s", w.Code, w.Body.String())
	}
}

func TestHandleCredentialsPage_AgentIDFilter(t *testing.T) {
	s := newCredentialsTestServer(t)

	entries := []db.CredentialEntry{
		{AgentID: "agent-A", Domain: "CORP", Username: "admin", Password: "pw1", Type: "cleartext", Source: "manual"},
		{AgentID: "agent-A", Domain: "CORP", Username: "backup", Password: "pw2", Type: "cleartext", Source: "manual"},
		{AgentID: "agent-B", Domain: "LAB", Username: "john", Hash: "aad3b435b51404eeaad3b435b51404ee", Type: "ntlm", Source: "mimikatz"},
	}
	if err := s.db.Create(&entries).Error; err != nil {
		t.Fatalf("seed credentials: %v", err)
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user_role", db.RoleAdmin)
	c.Request, _ = http.NewRequest(http.MethodGet, "/credentials?agent_id=agent-A&limit=1", nil)

	s.handleCredentialsPage(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Total        int                  `json:"total"`
		VaultCount   int                  `json:"vault_count"`
		VaultEntries []db.CredentialEntry `json:"vault_entries"`
		AgentFilter  string               `json:"agent_filter"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid json: %v; body=%s", err, w.Body.String())
	}
	if resp.AgentFilter != "agent-A" {
		t.Fatalf("agent_filter = %q, want agent-A", resp.AgentFilter)
	}
	if resp.Total != 2 {
		t.Fatalf("expected total=2 for agent-A, got %d", resp.Total)
	}
	if resp.VaultCount != 1 {
		t.Fatalf("expected vault_count=1 (limit=1), got %d", resp.VaultCount)
	}
	if len(resp.VaultEntries) != 1 {
		t.Fatalf("expected 1 vault entry (limit=1), got %d", len(resp.VaultEntries))
	}
	if resp.VaultEntries[0].AgentID != "agent-A" {
		t.Fatalf("expected entry for agent-A, got %q", resp.VaultEntries[0].AgentID)
	}
}

func TestHandleCredentialsPage_NoAgentFilter(t *testing.T) {
	s := newCredentialsTestServer(t)

	entries := []db.CredentialEntry{
		{AgentID: "agent-A", Username: "admin", Password: "pw1", Type: "cleartext", Source: "manual"},
		{AgentID: "agent-B", Username: "john", Hash: "aad3b435b51404eeaad3b435b51404ee", Type: "ntlm", Source: "mimikatz"},
	}
	if err := s.db.Create(&entries).Error; err != nil {
		t.Fatalf("seed credentials: %v", err)
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user_role", db.RoleAdmin)
	c.Request, _ = http.NewRequest(http.MethodGet, "/credentials", nil)

	s.handleCredentialsPage(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Total int `json:"total"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid json: %v; body=%s", err, w.Body.String())
	}
	if resp.Total != 2 {
		t.Fatalf("expected total=2 without filter, got %d", resp.Total)
	}
}
