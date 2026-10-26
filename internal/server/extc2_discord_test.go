package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	forgecrypto "github.com/forgec2/forgec2/internal/crypto"
	"github.com/forgec2/forgec2/internal/db"
)

// extC2DiscordFixture builds a Discord channel pointed at the test server.
func extC2DiscordFixture(t *testing.T) (*Server, *DiscordExternalC2) {
	t.Helper()
	s := mustTenantServer(t)
	seedMissingAPIAdmin(t, s)
	if err := s.db.Create(&db.Implant{ID: "extc2-agent", TenantID: 1, Hostname: "extc2-host", Status: "online"}).Error; err != nil {
		t.Fatal(err)
	}
	// The result MAC uses the extc2 key (an empty key fails every result
	// closed) and the result is encrypted at rest, so loot encryption must be
	// initialised too.
	s.cfg.Crypto.ExtC2Key = testStorageKeyHex
	forgecrypto.InitLootEncryption(testStorageKeyHex)
	d := NewDiscordExternalC2(s, "bot-token", "channel-1")
	return s, d
}

func extC2ResultBody(t *testing.T, agentID string, taskID uint, resultID, result, mac string) string {
	t.Helper()
	raw, err := json.Marshal(extC2WSMessage{
		Type:     "result",
		AgentID:  agentID,
		TaskID:   taskID,
		ResultID: resultID,
		Result:   result,
		HMAC:     mac,
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// TestExtC2ProcessExternalC2ResultGuards pins the state machine: only a pending
// or running task of the claimed agent may be completed, and a cancelled or
// finalised task must not be resurrected.
func TestExtC2ProcessExternalC2ResultGuards(t *testing.T) {
	s, _ := extC2DiscordFixture(t)

	pending := db.Task{AgentID: "extc2-agent", TenantID: 1, Type: "shell", Command: "whoami", Status: "pending"}
	cancelled := db.Task{AgentID: "extc2-agent", TenantID: 1, Type: "shell", Command: "whoami", Status: "cancelled"}
	completed := db.Task{AgentID: "extc2-agent", TenantID: 1, Type: "shell", Command: "whoami", Status: "completed"}
	for _, task := range []*db.Task{&pending, &cancelled, &completed} {
		if err := s.db.Create(task).Error; err != nil {
			t.Fatal(err)
		}
	}
	foreign := db.Task{AgentID: "other-agent", TenantID: 1, Type: "shell", Status: "pending"}
	if err := s.db.Create(&foreign).Error; err != nil {
		t.Fatal(err)
	}

	status := func(id uint) (string, string, string) {
		var row db.Task
		if err := s.db.Where("id = ?", id).First(&row).Error; err != nil {
			t.Fatalf("reload task %d: %v", id, err)
		}
		return row.Status, row.Result, row.Error
	}

	// taskID 0 and unknown tasks are ignored.
	s.processExternalC2Result("extc2-agent", 0, "r1", "out")
	s.processExternalC2Result("extc2-agent", 999999, "r1", "out")

	// Wrong agent: the task belongs to another agent, so nothing changes.
	s.processExternalC2Result("extc2-agent", foreign.ID, "r1", "out")
	if st, _, _ := status(foreign.ID); st != "pending" {
		t.Fatalf("foreign task status = %q", st)
	}

	// Cancelled stays cancelled; completed stays completed.
	s.processExternalC2Result("extc2-agent", cancelled.ID, "r1", "out")
	if st, _, _ := status(cancelled.ID); st != "cancelled" {
		t.Fatalf("cancelled task status = %q", st)
	}
	s.processExternalC2Result("extc2-agent", completed.ID, "r1", "out")
	if st, _, _ := status(completed.ID); st != "completed" {
		t.Fatalf("completed task status = %q", st)
	}

	// A pending task is completed and the result is encrypted at rest.
	s.processExternalC2Result("extc2-agent", pending.ID, "result-42", "the output")
	if st, _, _ := status(pending.ID); st != "completed" {
		t.Fatalf("pending task status = %q, want completed", st)
	}
	// GORM AfterFind decrypts Result transparently, so the model read returns
	// the plaintext; the raw column must hold ciphertext.
	var plain db.Task
	if err := s.db.Where("id = ?", pending.ID).First(&plain).Error; err != nil {
		t.Fatal(err)
	}
	if plain.Result != "the output" {
		t.Fatalf("decrypted result = %q", plain.Result)
	}
	var atRest string
	if err := s.db.Raw("SELECT result FROM tasks WHERE id = ?", pending.ID).Scan(&atRest).Error; err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(atRest, "FC2ENC:") {
		t.Fatalf("result stored in plaintext: %q", atRest)
	}
	if plain.LastResultID != "result-42" {
		t.Fatalf("last_result_id = %q", plain.LastResultID)
	}
}

// TestDiscordProcessMessageRequiresHMAC pins the property that channel
// membership is not authentication: a relayed result without a valid extc2
// HMAC must be dropped, while a correctly signed one is applied.
func TestDiscordProcessMessageRequiresHMAC(t *testing.T) {
	s, d := extC2DiscordFixture(t)

	task := db.Task{AgentID: "extc2-agent", TenantID: 1, Type: "shell", Status: "pending"}
	if err := s.db.Create(&task).Error; err != nil {
		t.Fatal(err)
	}

	// Not JSON, or not a result frame: ignored without panicking.
	d.processMessage("not json")
	d.processMessage(`{"type":"task","agent_id":"extc2-agent"}`)

	// A result with a bad MAC is dropped.
	bad := extC2ResultBody(t, "extc2-agent", task.ID, "r1", "output", "deadbeef")
	d.processMessage(bad)
	var row db.Task
	if err := s.db.Where("id = ?", task.ID).First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.Status != "pending" {
		t.Fatalf("a forged result completed the task (status=%q)", row.Status)
	}

	// The same frame with the correct MAC is accepted.
	mac := extC2ResultMAC(s.cfg.Crypto.ExtC2Key, "extc2-agent", task.ID, "r1", "output")
	good := extC2ResultBody(t, "extc2-agent", task.ID, "r1", "output", mac)
	d.processMessage(good)
	if err := s.db.Where("id = ?", task.ID).First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.Status != "completed" {
		t.Fatalf("signed result not applied (status=%q)", row.Status)
	}
}

// TestDiscordIsRunningAndStop covers the lifecycle flags.
func TestDiscordIsRunningAndStop(t *testing.T) {
	_, d := extC2DiscordFixture(t)
	if d.IsRunning() {
		t.Fatal("a fresh channel reports running")
	}
	d.running = true
	if !d.IsRunning() {
		t.Fatal("IsRunning false after being marked running")
	}
	d.Stop()
	if d.IsRunning() {
		t.Fatal("Stop left the channel running")
	}
}

var _ = sha256.New
var _ = hex.EncodeToString
var _ = http.StatusOK
var _ = time.Now
