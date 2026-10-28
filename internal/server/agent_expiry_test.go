package server

import (
	"net/http"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/db"
)

// TestExpiredAgentRefusesTasks covers the engagement deadline: a deadline that exists only on the
// operator's calendar is not a control, so an implant built from a secret with an expiry stops
// accepting work when it passes - and the refusal is both explicit (403, not a 500 and not silence) and
// audited.
//
// The deadline is enforced at the single creation gate (validateTaskCreationWithAgent), which is what
// every path - handlers, bulk actions, automation, scripts - goes through, so no path can disagree.
func TestExpiredAgentRefusesTasks(t *testing.T) {
	s := newTasksTestServer(t)

	past := time.Now().Add(-time.Hour)
	future := time.Now().Add(time.Hour)

	seed := func(id string, expires *time.Time) {
		if err := s.db.Create(&db.Implant{ID: id, Hostname: id, Status: "online", LastSeen: time.Now(), ExpiresAt: expires}).Error; err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	seed("expired-agent", &past)
	seed("live-agent", &future)
	seed("unbounded-agent", nil)

	// 1. An expired implant refuses task creation with a 403-class error that names the deadline.
	err := s.validateTaskCreationWithAgent(nil, "expired-agent", "shell", "whoami", "", "", 0, nil)
	if err == nil {
		t.Fatal("an expired implant accepted a task")
	}
	if status, ok := clientErrorStatus(err); !ok || status != http.StatusForbidden {
		t.Fatalf("expired refusal maps to %d (mapped=%v), want 403: %v", status, ok, err)
	}

	// 2. A live deadline and no deadline both work: the feature must not change existing installs.
	for _, agentID := range []string{"live-agent", "unbounded-agent"} {
		if err := s.validateTaskCreationWithAgent(nil, agentID, "shell", "whoami", "", "", 0, nil); err != nil {
			t.Fatalf("%s was refused: %v", agentID, err)
		}
	}

	// 3. The refusal is audited: "why did this stop working" must be answerable after the fact.
	var audited int64
	if err := s.db.Model(&db.AuditLog{}).Where("action = ?", "agent_expired_block").Count(&audited).Error; err != nil {
		t.Fatalf("count audit: %v", err)
	}
	if audited != 1 {
		t.Fatalf("agent_expired_block rows = %d, want 1", audited)
	}
}

// TestRegSecretExpiryIsInherited proves the deadline travels with the build rather than being configured
// per agent afterwards.
func TestRegSecretExpiryIsInherited(t *testing.T) {
	s := newTasksTestServer(t)
	deadline := time.Now().Add(48 * time.Hour).UTC().Truncate(time.Second)

	if err := s.db.Create(&db.RegSecret{ID: "secret-with-deadline", ExpiresAt: &deadline}).Error; err != nil {
		t.Fatalf("seed secret: %v", err)
	}
	if got := s.regSecretExpiry("secret-with-deadline"); got == nil || !got.Equal(deadline) {
		t.Fatalf("regSecretExpiry = %v, want %v", got, deadline)
	}
	// No such secret, and an empty id, both mean "unbounded" rather than an error.
	if got := s.regSecretExpiry("does-not-exist"); got != nil {
		t.Fatalf("a missing secret produced %v, want nil", got)
	}
	if got := s.regSecretExpiry(""); got != nil {
		t.Fatalf("an empty secret id produced %v, want nil", got)
	}
}
