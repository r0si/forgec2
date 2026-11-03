package server

import (
	"strings"
	"testing"

	"github.com/forgec2/forgec2/internal/db"
)

// TestAuditAIToolCall verifies every authorized AI tool invocation lands in
// the audit trail with secret-redacted arguments.
func TestAuditAIToolCall(t *testing.T) {
	s := setupAIRunStorageTest(t)
	if err := s.db.AutoMigrate(&db.AuditLog{}); err != nil {
		t.Fatal(err)
	}
	ctx := &aiReqCtx{
		Principal: aiPrincipal{UserID: 10, Username: "alice", TenantID: 1, Role: db.RoleAdmin},
		SessionID: 7,
		RunID:     "run-1",
	}
	s.auditAIToolCall(ctx, "token_make", `{"user":"bob","password":"s3cret"}`)
	var entries []db.AuditLog
	if err := s.db.Where("action = ?", "ai_tool").Find(&entries).Error; err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 ai_tool audit entry, got %d", len(entries))
	}
	e := entries[0]
	if e.User != "alice" {
		t.Fatalf("audit user = %q, want alice", e.User)
	}
	if !strings.Contains(e.Details, "token_make") || !strings.Contains(e.Details, "session_id:7") {
		t.Fatalf("audit details missing tool/session: %q", e.Details)
	}
	if strings.Contains(e.Details, "s3cret") {
		t.Fatalf("audit details leaked secret: %q", e.Details)
	}
	// Nil context must not panic and attributes to "ai".
	s.auditAIToolCall(nil, "list_agents", `{}`)
}
