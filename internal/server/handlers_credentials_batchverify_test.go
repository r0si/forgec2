package server

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/forgec2/forgec2/internal/db"
)

func seedCredAgent(t *testing.T, s *Server, id string, tenantID uint) {
	t.Helper()
	if err := s.db.Create(&db.Implant{ID: id, Hostname: id, TenantID: tenantID, Status: "online"}).Error; err != nil {
		t.Fatalf("seed agent %s: %v", id, err)
	}
}

func seedCredEntry(t *testing.T, s *Server, agentID, domain, username, password string, tenantID uint) uint {
	t.Helper()
	entry := db.CredentialEntry{
		AgentID:  agentID,
		Domain:   domain,
		Username: username,
		Password: password,
		TenantID: tenantID,
	}
	if err := s.db.Create(&entry).Error; err != nil {
		t.Fatalf("seed credential %s: %v", username, err)
	}
	return entry.ID
}

// credBatchServer builds a server with an operator whose tenant is actually
// resolvable, plus the pending-task counters initialized.
//
// Both details are required and neither is a product bug. resolveTenant looks
// the caller up by username and treats an unknown user as a legacy unscoped
// operator (tenant 0, global visibility), so a test that never seeds the user
// silently loses the tenant boundary it is meant to assert. And
// newCredentialsTestServer builds &Server{...} directly instead of via
// NewServer, leaving agentPendingTasks a nil map that the first queued task
// panics on. Production only reaches a Server through NewServer (server.go).
func credBatchServer(t *testing.T) *Server {
	t.Helper()
	s := newCredentialsTestServer(t)
	s.agentPendingTasks = make(map[string]int)
	if err := s.db.Create(&db.User{Username: "bv-alice", Role: "admin", TenantID: 1, IsActive: true}).Error; err != nil {
		t.Fatalf("seed tenant-1 operator: %v", err)
	}
	if err := s.db.Create(&db.User{Username: "bv-bob", Role: "admin", TenantID: 2, IsActive: true}).Error; err != nil {
		t.Fatalf("seed tenant-2 operator: %v", err)
	}
	return s
}

func batchVerifyBody(t *testing.T, s *Server, username string, ids []uint) []map[string]any {
	t.Helper()
	idList := make([]string, 0, len(ids))
	for _, id := range ids {
		idList = append(idList, fmt.Sprint(id))
	}
	c, w := tenantCtx(http.MethodPost, "/x", username, "")
	c.Request, _ = http.NewRequest(http.MethodPost, "/x",
		strings.NewReader(fmt.Sprintf(`{"ids":[%s]}`, strings.Join(idList, ","))))
	c.Request.Header.Set("Content-Type", "application/json")
	s.handleBatchVerifyCredentials(c)
	if w.Code != http.StatusOK {
		t.Fatalf("batch verify: status %d body=%s", w.Code, w.Body.String())
	}
	var body struct {
		Results []map[string]any `json:"results"`
	}
	unmarshalData(t, w.Body.Bytes(), &body)
	return body.Results
}

// TestBatchVerifyCredentialsAgentLookupIsBatched pins the property the N+1 fix
// is about: the agent-existence gate resolves the whole batch in one query.
// The assertion counts multi-id (IN) lookups, because createTask independently
// queries the implants table per queued task and would otherwise swamp the
// count.
func TestBatchVerifyCredentialsAgentLookupIsBatched(t *testing.T) {
	s := credBatchServer(t)
	seedCredAgent(t, s, "bv-1", 1)
	seedCredAgent(t, s, "bv-2", 1)

	// 24 cleartext entries over 2 agents: the per-row gate queried the same two
	// agents 24 times.
	var ids []uint
	for i := 0; i < 24; i++ {
		agent := "bv-1"
		if i%2 == 1 {
			agent = "bv-2"
		}
		ids = append(ids, seedCredEntry(t, s, agent, "CORP", fmt.Sprintf("user%d", i), "p@ssw0rd!", 1))
	}

	served := withSQLCounter(t, s)
	batchVerifyBody(t, s, "bv-alice", ids)

	// Asserted as "exactly one multi-id lookup" rather than "few queries":
	// a "fewer than N" bound is satisfied by zero, which is what a reverted
	// per-row gate produces (it issues only `id = ?` queries). createTask also
	// reads the implants table once per queued task, so an absolute implant
	// count cannot be the assertion either.
	if _, _, batched := served(); batched != 1 {
		t.Fatalf("multi-id agent lookups = %d, want exactly 1 (per-row gate issues none)", batched)
	}
}

// TestBatchVerifyCredentialsAgentGoneIsSkipped proves the batched gate still
// refuses agents that no longer exist — the branch a batching rewrite most
// easily loses, since "absent from the map" has to mean unreachable.
func TestBatchVerifyCredentialsAgentGoneIsSkipped(t *testing.T) {
	s := credBatchServer(t)
	seedCredAgent(t, s, "bv-alive", 1)
	alive := seedCredEntry(t, s, "bv-alive", "CORP", "alice", "p@ssw0rd!", 1)
	gone := seedCredEntry(t, s, "bv-deleted", "CORP", "bob", "p@ssw0rd!", 1)

	results := batchVerifyBody(t, s, "bv-alice", []uint{alive, gone})
	got := map[uint]string{}
	for _, r := range results {
		got[uint(r["id"].(float64))], _ = r["status"].(string)
	}
	if got[alive] != "queued" {
		t.Fatalf("live agent: status = %q, want queued (%v)", got[alive], results)
	}
	if got[gone] != "skipped_agent_gone" {
		t.Fatalf("missing agent: status = %q, want skipped_agent_gone (%v)", got[gone], results)
	}
}

// TestBatchVerifyCredentialsForeignAgentIsNotQueued is the security property the
// batched lookup must preserve: a tenant-1 credential row pointing at a
// tenant-2 agent must not produce a cred_check task, because that task carries
// this tenant's vault password to a foreign host.
func TestBatchVerifyCredentialsForeignAgentIsNotQueued(t *testing.T) {
	s := credBatchServer(t)
	seedCredAgent(t, s, "bv-t1-agent", 1)
	seedCredAgent(t, s, "bv-t2-agent", 2)
	own := seedCredEntry(t, s, "bv-t1-agent", "CORP", "alice", "p@ssw0rd!", 1)
	// A tenant-1 credential row that (wrongly) points at a tenant-2 agent.
	foreignRef := seedCredEntry(t, s, "bv-t2-agent", "CORP", "bob", "p@ssw0rd!", 1)

	results := batchVerifyBody(t, s, "bv-alice", []uint{own, foreignRef})
	for _, r := range results {
		if uint(r["id"].(float64)) == foreignRef && r["status"] == "queued" {
			t.Fatalf("cred_check queued against a foreign-tenant agent: %v", results)
		}
	}

	var credChecks int64
	s.db.Model(&db.Task{}).Where("type = ?", "cred_check").Count(&credChecks)
	if credChecks != 1 {
		t.Fatalf("cred_check tasks = %d, want 1 (own agent only)", credChecks)
	}
}

// TestVerifiableAgentIDsAppliesTheSameSkips guards the batch input: it must
// mirror the handler's skip chain, or the lookup queries agents whose entries
// never needed checking and can drift away from the gate itself.
func TestVerifiableAgentIDsAppliesTheSameSkips(t *testing.T) {
	got := verifiableAgentIDs([]db.CredentialEntry{
		{ID: 1, AgentID: "a", Password: "p", Hash: "h"},
		{ID: 2, AgentID: "a", Password: "p"}, // duplicate agent
		{ID: 3, AgentID: "b", Hash: "h"},     // hash-only: no cleartext
		{ID: 4, Password: "p"},               // no agent
		{ID: 5, AgentID: "c"},                // neither password nor hash
		{ID: 6, AgentID: "d", Password: "p"},
	})
	want := []string{"a", "d"}
	if len(got) != len(want) {
		t.Fatalf("verifiableAgentIDs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("verifiableAgentIDs = %v, want %v (first-seen order)", got, want)
		}
	}
}
