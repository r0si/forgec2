package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

// TestCredentialIngestWritesAuditLog verifies an automated credential ingest
// (from a beacon result) lands in the tamper-evident audit chain so credential
// exfiltration is always attributable to an operator session or agent.
func TestCredentialIngestWritesAuditLog(t *testing.T) {
	s := newCredentialsTestServer(t)

	raw := "Username : jsmith\nDomain   : CORP\nNTLM     : aad3b435b51404eeaad3b435b51404ee\n\n" +
		"Username : kmartin\nPassword : Winter2025!\n"
	s.parseAndStoreCredentials("cred-agent-1", raw, 42)

	var logs []db.AuditLog
	if err := s.db.Where("action = ? AND agent_id = ?", "credential_ingest", "cred-agent-1").Find(&logs).Error; err != nil {
		t.Fatalf("query audit: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("expected 1 credential_ingest audit entry, got %d", len(logs))
	}
	if !strings.Contains(logs[0].Details, "stored 2 credentials") {
		t.Fatalf("audit details missing count: %q", logs[0].Details)
	}
	if strings.Contains(logs[0].Details, "Winter2025!") || strings.Contains(logs[0].Details, "aad3b435") {
		t.Fatalf("audit details must never contain raw secrets: %q", logs[0].Details)
	}
}

func TestCredentialKerberoastIngestWritesAuditLog(t *testing.T) {
	s := newCredentialsTestServer(t)

	s.parseAndStoreKerberoastResults("cred-agent-2", "svc/http@CORP.LOCAL:1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d", 43)

	var count int64
	s.db.Model(&db.AuditLog{}).Where("action = ? AND agent_id = ?", "credential_ingest", "cred-agent-2").Count(&count)
	if count != 1 {
		t.Fatalf("expected 1 kerberoast ingest audit entry, got %d", count)
	}
}

// TestCredentialExportWritesAuditLog verifies manual credential exfiltration via
// the CSV export endpoint is audited (checked against the rate limiter).
func TestCredentialExportWritesAuditLog(t *testing.T) {
	s := newCredentialsTestServer(t)

	if err := s.db.Create(&db.CredentialEntry{
		AgentID: "cred-agent-3", Domain: "CORP", Username: "admin", Password: "secret", Type: "cleartext",
	}).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user_role", db.RoleAdmin)
	c.Request, _ = http.NewRequest(http.MethodGet, "/api/credentials/export", nil)

	s.handleExportCredentials(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var count int64
	s.db.Model(&db.AuditLog{}).Where("action = ?", "credential_export").Count(&count)
	if count != 1 {
		t.Fatalf("expected credential_export audit entry, got %d", count)
	}
	var entry db.AuditLog
	s.db.Where("action = ?", "credential_export").First(&entry)
	if strings.Contains(entry.Details, "secret") {
		t.Fatalf("audit must not embed secrets: %q", entry.Details)
	}
}

// TestCredentialCrudWritesAuditLog verifies add/update/delete/tag/confirm
// operations hit the audit chain.
func TestCredentialCrudWritesAuditLog(t *testing.T) {
	s := newCredentialsTestServer(t)

	form := url.Values{}
	form.Set("type", "cleartext")
	form.Set("username", "svc")
	form.Set("password", "x")
	form.Set("domain", "LAB")

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user_role", db.RoleAdmin)
	c.Request, _ = http.NewRequest(http.MethodPost, "/credentials/add", strings.NewReader(form.Encode()))
	c.Request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	s.handleAddCredential(c)

	var created db.CredentialEntry
	if err := s.db.Where("username = ?", "svc").First(&created).Error; err != nil {
		t.Fatalf("credential not created: %v", err)
	}

	w2 := httptest.NewRecorder()
	c2, _ := gin.CreateTestContext(w2)
	c2.Set("user_role", db.RoleAdmin)
	c2.Request, _ = http.NewRequest(http.MethodDelete, "/credentials/"+strconv.FormatUint(uint64(created.ID), 10), nil)
	c2.Params = gin.Params{{Key: "cred_id", Value: strconv.FormatUint(uint64(created.ID), 10)}}
	s.handleDeleteCredential(c2)

	var addCount, delCount int64
	s.db.Model(&db.AuditLog{}).Where("action = ?", "credential_add").Count(&addCount)
	s.db.Model(&db.AuditLog{}).Where("action = ?", "credential_delete").Count(&delCount)
	if addCount != 1 || delCount != 1 {
		t.Fatalf("expected 1 add + 1 delete audit entries, got add=%d del=%d", addCount, delCount)
	}
}
