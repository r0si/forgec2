package server

import (
	"testing"

	"github.com/forgec2/forgec2/internal/db"
)

// TestParseAndStoreCredentialsDedupes pins the harvesting contract: the parsed
// rows are attributed to the agent's tenant, duplicates inside one harvest and
// across repeated harvests are collapsed, and a re-harvest adds nothing.
func TestParseAndStoreCredentialsDedupes(t *testing.T) {
	s := setupToolExecServer(t)
	seedToolExecAgents(t, s) // a-win belongs to tenant 1

	raw := "alice:s3cret\nCORP\\bob:pa55w0rd\nbob:pa55w0rd\nnoise that is not a credential\n"
	s.parseAndStoreCredentials("a-win", raw, 1)

	var rows []db.CredentialEntry
	if err := s.db.Where("agent_id = ?", "a-win").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	// 3 rows: alice, the unqualified bob, and CORP\bob. The exact duplicate
	// bob line is collapsed, while the domain-qualified principal is a
	// different identity and must be kept.
	if len(rows) != 3 {
		t.Fatalf("stored %d credentials, want 3 (exact duplicate collapsed): %+v", len(rows), rows)
	}
	domains := map[string]bool{}
	for _, r := range rows {
		domains[r.Domain+"/"+r.Username] = true
	}
	for _, want := range []string{"/alice", "/bob", "CORP/bob"} {
		if !domains[want] {
			t.Fatalf("missing identity %q in %v", want, domains)
		}
	}
	for _, r := range rows {
		if r.TenantID != 1 {
			t.Fatalf("credential %q tenant = %d, want the agent's tenant", r.Username, r.TenantID)
		}
		if r.Password == "" {
			t.Fatalf("credential %q stored without a password", r.Username)
		}
	}

	// A second harvest of the same text must add nothing.
	s.parseAndStoreCredentials("a-win", raw, 2)
	var after int64
	s.db.Model(&db.CredentialEntry{}).Where("agent_id = ?", "a-win").Count(&after)
	if after != 3 {
		t.Fatalf("re-harvest grew the vault to %d rows", after)
	}

	// A new identity is still stored on top of the existing ones.
	s.parseAndStoreCredentials("a-win", "carol:newpass", 3)
	s.db.Model(&db.CredentialEntry{}).Where("agent_id = ?", "a-win").Count(&after)
	if after != 4 {
		t.Fatalf("new credential not stored, count = %d", after)
	}
}

// TestParseAndStoreCredentialsIgnoresEmptyInput covers the early return.
func TestParseAndStoreCredentialsIgnoresEmptyInput(t *testing.T) {
	s := setupToolExecServer(t)
	seedToolExecAgents(t, s)

	s.parseAndStoreCredentials("a-win", "", 1)
	s.parseAndStoreCredentials("a-win", "\n\n\n", 1)
	var count int64
	s.db.Model(&db.CredentialEntry{}).Where("agent_id = ?", "a-win").Count(&count)
	if count != 0 {
		t.Fatalf("empty harvest stored %d rows", count)
	}
}

// TestAgentTenantID covers the attribution helper, including the orphan case.
func TestAgentTenantID(t *testing.T) {
	s := setupToolExecServer(t)
	seedToolExecAgents(t, s)

	if got := s.agentTenantID("a-win"); got != 1 {
		t.Fatalf("a-win tenant = %d, want 1", got)
	}
	if got := s.agentTenantID("does-not-exist"); got != 0 {
		t.Fatalf("orphan agent tenant = %d, want 0 so the write hook falls back", got)
	}
}
