package server

import (
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"gorm.io/gorm"
)

// TestProcessAgentRegistrationBlockedRefusesService pins the force-offline
// contract: a blocked implant is pinned offline, every check-in is audited, and
// the caller gets no agent — so nothing is delivered to it and nothing it sends
// is accepted. Beaconing never un-blocks a row (unlike a soft delete).
func TestProcessAgentRegistrationBlockedRefusesService(t *testing.T) {
	s := mustTenantServer(t)
	blocked := db.Implant{
		ID: "blocked-agent", TenantID: 1, Hostname: "h", Status: "online",
		Blocked: true, BlockedReason: "rules of engagement", LastSeen: time.Now(),
	}
	if err := s.db.Create(&blocked).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Model(&db.Implant{}).Where("id = ?", blocked.ID).Update("blocked", true).Error; err != nil {
		t.Fatal(err)
	}

	// The second return value means "was this a new agent", not "accepted", so
	// refusal is signalled by an empty implant ID (the caller checks that).
	agent, _ := s.processAgentRegistration(beaconRequest{
		UUID: "blocked-agent", Info: map[string]string{"hostname": "h"},
	}, "203.0.113.9", time.Now())
	if agent.ID != "" {
		t.Fatalf("a blocked agent produced an implant: %+v", agent)
	}

	// Pinned offline and still blocked (beaconing must not resurrect it).
	var reloaded db.Implant
	if err := s.db.Where("id = ?", blocked.ID).First(&reloaded).Error; err != nil {
		t.Fatal(err)
	}
	if reloaded.Status != "offline" {
		t.Fatalf("status = %q, want offline", reloaded.Status)
	}
	if !reloaded.Blocked {
		t.Fatal("the blocked flag was cleared by a check-in")
	}

	// The refusal is in the audit trail, with the reason.
	var audits []db.AuditLog
	if err := s.db.Where("action = ?", "blocked_agent_checkin").Find(&audits).Error; err != nil {
		t.Fatal(err)
	}
	if len(audits) == 0 {
		t.Fatal("the blocked check-in was not audited")
	}
	if !containsSubstring(audits[0].Details, "rules of engagement") {
		t.Fatalf("audit details = %q, want the blocked reason", audits[0].Details)
	}
}

// TestProcessAgentRegistrationRestoresSoftDeletedAgent pins the tombstone rule:
// an agent removed from the console that beacons again is restored rather than
// left as a row that blocks re-registration (which would lock it out forever).
func TestProcessAgentRegistrationRestoresSoftDeletedAgent(t *testing.T) {
	s := mustTenantServer(t)
	gone := db.Implant{ID: "gone-agent", TenantID: 1, Hostname: "gone", Status: "offline", LastSeen: time.Now()}
	if err := s.db.Create(&gone).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Delete(&db.Implant{}, "id = ?", gone.ID).Error; err != nil {
		t.Fatal(err)
	}
	var tombstone db.Implant
	if err := s.db.Unscoped().Where("id = ?", gone.ID).First(&tombstone).Error; err != nil {
		t.Fatal(err)
	}
	if !tombstone.DeletedAt.Valid {
		t.Fatal("setup did not soft-delete the row")
	}

	agent, isNew := s.processAgentRegistration(beaconRequest{
		UUID: "gone-agent", Info: map[string]string{"hostname": "gone"},
	}, "203.0.113.9", time.Now())
	if agent.ID != "gone-agent" {
		t.Fatalf("a returning soft-deleted agent was refused: %+v", agent)
	}
	if isNew {
		t.Fatal("a restored agent was reported as new")
	}
	var after db.Implant
	if err := s.db.Where("id = ?", gone.ID).First(&after).Error; err != nil {
		t.Fatalf("the row is still soft-deleted: %v", err)
	}
	if after.DeletedAt.Valid {
		t.Fatal("the tombstone was not cleared")
	}
}

// TestProcessAgentRegistrationRejectsGhost pins the new-agent guard: an
// enrollment with neither hostname nor IP is not a real implant.
func TestProcessAgentRegistrationRejectsGhost(t *testing.T) {
	s := mustTenantServer(t)
	if agent, _ := s.processAgentRegistration(beaconRequest{
		UUID: "ghost", Info: map[string]string{},
	}, "", time.Now()); agent.ID != "" {
		t.Fatal("a ghost registration (no hostname, no ip) was accepted")
	}
	var count int64
	s.db.Model(&db.Implant{}).Where("id = ?", "ghost").Count(&count)
	if count != 0 {
		t.Fatal("a ghost registration created a row")
	}
}

func containsSubstring(haystack, needle string) bool {
	return len(needle) == 0 || (len(haystack) >= len(needle) && indexOfSub(haystack, needle) >= 0)
}

func indexOfSub(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

var _ = gorm.ErrRecordNotFound
