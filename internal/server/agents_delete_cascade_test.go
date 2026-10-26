package server

import (
	"testing"
	"time"

	forgecrypto "github.com/forgec2/forgec2/internal/crypto"

	"github.com/forgec2/forgec2/internal/db"
	"gorm.io/gorm"
)

// TestDeleteAgentCascadeCoversEveryTable seeds one row in each table that
// references an agent, deletes the agent, and requires every dependent row to
// be gone — while another agent's rows survive. A table missed here is an
// orphan-row bug that only shows up much later (stale mesh nodes, resurrected
// credentials).
func TestDeleteAgentCascadeCoversEveryTable(t *testing.T) {
	s := mustTenantServer(t)
	forgecryptoInitForTest(t)
	seedAIUser(t, s, "admin1", 1)
	for _, id := range []string{"victim", "bystander"} {
		if err := s.db.Create(&db.Implant{ID: id, TenantID: 1, Hostname: id, Status: "online", LastSeen: time.Now()}).Error; err != nil {
			t.Fatal(err)
		}
	}

	now := time.Now()
	seed := func(agentID string) {
		mustCreate(t, s.db.Create(&db.Task{AgentID: agentID, TenantID: 1, Type: "shell", Command: "whoami", Status: "completed", CreatedAt: now}))
		mustCreate(t, s.db.Create(&db.CredentialEntry{AgentID: agentID, TenantID: 1, Username: "u", Password: "p", Type: "cleartext"}))
		mustCreate(t, s.db.Create(&db.TokenEntry{AgentID: agentID, Username: "u", Domain: "CORP", LogonType: "Network"}))
		mustCreate(t, s.db.Create(&db.SocksSession{AgentID: agentID, ListenPort: 1080, Status: "active"}))
		mustCreate(t, s.db.Create(&db.ScanResult{AgentID: agentID, TargetIP: "10.0.0.1", Port: 80, Protocol: "tcp", State: "open"}))
		mustCreate(t, s.db.Create(&db.NetworkHost{AgentID: agentID, IP: "10.0.0.1", Hostname: "h", LastSeen: now}))
		mustCreate(t, s.db.Create(&db.AgentTagAssignment{ImplantID: agentID, AgentTagID: "tag-" + agentID}))
		mustCreate(t, s.db.Create(&db.AgentGroupAssignment{ImplantID: agentID, AgentGroupID: "group-" + agentID}))
		mustCreate(t, s.db.Create(&db.Notification{AgentID: agentID, Type: "info", Title: "t", Message: "m"}))
		mustCreate(t, s.db.Create(&db.AgentLock{ID: "lock-" + agentID, AgentID: agentID, LockedBy: "admin1", LockedAt: now, ExpiresAt: now.Add(time.Hour)}))
		mustCreate(t, s.db.Create(&db.OpsecHistory{AgentID: agentID, TaskType: "shell", RuleName: "r", Message: "m"}))
		mustCreate(t, s.db.Create(&db.BloodHoundResult{AgentID: agentID, CollectionMethod: "sharp", Summary: "s"}))
		mustCreate(t, s.db.Create(&db.MeshPeer{AgentID: agentID, PeerID: agentID + "-peer"}))
	}
	seed("victim")
	seed("bystander")

	if !s.deleteAgentRecord("victim") {
		t.Fatal("deleteAgentRecord reported failure")
	}

	countFor := func(model any, column, id string) int64 {
		var n int64
		if err := s.db.Model(model).Where(column+" = ?", id).Count(&n).Error; err != nil {
			t.Fatalf("count %T: %v", model, err)
		}
		return n
	}

	checks := []struct {
		name   string
		model  any
		column string
	}{
		{"tasks", &db.Task{}, "agent_id"},
		{"credentials", &db.CredentialEntry{}, "agent_id"},
		{"tokens", &db.TokenEntry{}, "agent_id"},
		{"socks sessions", &db.SocksSession{}, "agent_id"},
		{"scan results", &db.ScanResult{}, "agent_id"},
		{"network hosts", &db.NetworkHost{}, "agent_id"},
		{"tag assignments", &db.AgentTagAssignment{}, "implant_id"},
		{"group assignments", &db.AgentGroupAssignment{}, "implant_id"},
		{"notifications", &db.Notification{}, "agent_id"},
		{"agent locks", &db.AgentLock{}, "agent_id"},
		{"opsec history", &db.OpsecHistory{}, "agent_id"},
		{"bloodhound results", &db.BloodHoundResult{}, "agent_id"},
		{"mesh peers", &db.MeshPeer{}, "agent_id"},
	}
	for _, c := range checks {
		if n := countFor(c.model, c.column, "victim"); n != 0 {
			t.Fatalf("%s: %d row(s) survived the agent delete", c.name, n)
		}
		if n := countFor(c.model, c.column, "bystander"); n != 1 {
			t.Fatalf("%s: deleted the bystander's row too (%d left)", c.name, n)
		}
	}

	// The implant itself is hard-deleted (not soft-deleted).
	var implants int64
	s.db.Unscoped().Model(&db.Implant{}).Where("id = ?", "victim").Count(&implants)
	if implants != 0 {
		t.Fatalf("implant row survived: %d", implants)
	}

	// Mesh rows where the victim was the *peer* are removed too.
	mustCreate(t, s.db.Create(&db.MeshPeer{AgentID: "bystander", PeerID: "victim"}))
	if !s.deleteAgentRecord("bystander") {
		t.Fatal("second delete failed")
	}
	var peers int64
	s.db.Model(&db.MeshPeer{}).Where("peer_id = ?", "victim").Count(&peers)
	if peers != 0 {
		t.Fatalf("a mesh row with the victim as peer survived: %d", peers)
	}
}

func mustCreate(t *testing.T, tx *gorm.DB) {
	t.Helper()
	if tx.Error != nil {
		t.Fatalf("seed row: %v", tx.Error)
	}
}

// forgecryptoInitForTest initialises loot encryption when a test seeds
// encrypted columns.
func forgecryptoInitForTest(t *testing.T) {
	t.Helper()
	forgecrypto.InitLootEncryption(testStorageKeyHex)
}
