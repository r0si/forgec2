package server

import (
	"testing"

	"github.com/google/uuid"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/pkg/protocol"
)

// TestProcessBeaconWithBudgetReplayIsEmpty pins replay protection: a beacon that
// matches one recently processed returns an EMPTY response, so a replayed frame
// cannot make the server hand out the same tasks again (which would run them
// twice on the target).
func TestProcessBeaconWithBudgetReplayIsEmpty(t *testing.T) {
	s := mustTenantServer(t)
	req := beaconRequest{
		UUID: replayAgentID, Seq: 1, ProtocolVersion: currentProtocolVersionForTest(),
		Info: map[string]string{"hostname": "replay"},
	}

	// Seed the dedupe cache with this exact frame, as if it had just been served.
	fp := beaconFingerprint(req)
	s.beaconDedupMu.Lock()
	if s.beaconDedupCache == nil {
		s.beaconDedupCache = make(map[string]time.Time)
	}
	s.beaconDedupCache[fp] = time.Now()
	s.beaconDedupMu.Unlock()

	resp := s.processBeaconWithBudget(req, "203.0.113.9", 0, 0)
	if len(resp.Tasks) != 0 {
		t.Fatalf("a replayed beacon was handed %d task(s)", len(resp.Tasks))
	}

	// No agent row was created by the replay.
	var count int64
	s.db.Model(&db.Implant{}).Where("id = ?", replayAgentID).Count(&count)
	if count != 0 {
		t.Fatal("a replayed beacon created an agent row")
	}
}

// TestProcessBeaconWithBudgetRejectsInvalidAgentID pins the input guard: a
// malformed agent id is dropped before any database or filesystem use.
func TestProcessBeaconWithBudgetRejectsInvalidAgentID(t *testing.T) {
	s := mustTenantServer(t)
	for _, id := range []string{"", "not-a-uuid", "../../etc/passwd", "ws_"} {
		resp := s.processBeaconWithBudget(beaconRequest{
			UUID: id, Seq: 1, ProtocolVersion: currentProtocolVersionForTest(),
			Info: map[string]string{"hostname": "x"},
		}, "203.0.113.9", 0, 0)
		if len(resp.Tasks) != 0 {
			t.Fatalf("invalid agent id %q was served tasks", id)
		}
	}
	var count int64
	s.db.Model(&db.Implant{}).Where("hostname = ?", "x").Count(&count)
	if count != 0 {
		t.Fatal("an invalid agent id created an agent row")
	}
}

// TestProcessBeaconWithBudgetProtocolRejection pins the negotiation refusal: an
// agent speaking an unsupported protocol gets a response that explains the
// mismatch instead of being silently dropped (which would look "offline").
func TestProcessBeaconWithBudgetProtocolRejection(t *testing.T) {
	s := mustTenantServer(t)
	resp := s.processBeaconWithBudget(beaconRequest{
		UUID: protoAgentID, Seq: 1, ProtocolVersion: 9999,
		Info: map[string]string{"hostname": "proto"},
	}, "203.0.113.9", 0, 0)
	if !resp.ProtocolRejected {
		t.Fatal("an unsupported protocol version was not rejected")
	}
	if resp.ProtocolRejectionReason == "" {
		t.Fatal("the rejection carries no explanation")
	}
	if len(resp.Tasks) != 0 {
		t.Fatalf("a rejected agent was handed %d task(s)", len(resp.Tasks))
	}
}

// Valid agent ids: isValidAgentID rejects anything that is not a canonical UUID
// (or a 32-char hex form), so a placeholder string would make every test below
// return on the input guard instead of reaching the branch under test.
var (
	replayAgentID = uuid.NewString()
	protoAgentID  = uuid.NewString()
)

// currentProtocolVersionForTest returns a protocol version the server accepts, so
// the tests above reach the branch they are aiming at instead of the
// negotiation refusal.
func currentProtocolVersionForTest() uint { return protocol.CurrentProtocolVersion }

// TestProcessBeaconWithBudgetAcceptsValidBeacon is the control for the three
// tests above: with a valid UUID, no seeded fingerprint and the current protocol
// version, the beacon reaches registration and the agent row is created. Without
// this, every other case in this file could be passing on an early return.
func TestProcessBeaconWithBudgetAcceptsValidBeacon(t *testing.T) {
	s := mustTenantServer(t)
	id := uuid.NewString()
	resp := s.processBeaconWithBudget(beaconRequest{
		UUID: id, Seq: 1, ProtocolVersion: currentProtocolVersionForTest(),
		Info: map[string]string{"hostname": "control-host"},
	}, "203.0.113.9", 0, 0)
	if resp.ProtocolRejected {
		t.Fatalf("a current-version beacon was rejected: %s", resp.ProtocolRejectionReason)
	}
	var stored db.Implant
	if err := s.db.Where("id = ?", id).First(&stored).Error; err != nil {
		t.Fatalf("the control beacon did not register the agent: %v", err)
	}
	if stored.Hostname != "control-host" {
		t.Fatalf("hostname = %q", stored.Hostname)
	}
}
