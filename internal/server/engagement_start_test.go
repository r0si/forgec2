package server

import (
	"net/http"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/db"
)

// TestEngagementStartBoundsWork covers the symmetric half of the engagement window.
//
// The end of the window was enforced in round 22; the start is the left bound, and without it an operator
// can task a host before the exercise has begun - which means the "agreed window" bounds nothing on the
// side where a premature action is most likely (a rehearsal run the day before, or a test at the wrong
// site).
func TestEngagementStartBoundsWork(t *testing.T) {
	s := newTasksTestServer(t)
	now := time.Now().UTC().Truncate(time.Second)
	day := 24 * time.Hour

	future := db.Engagement{TenantID: 1, Name: "starts tomorrow", StartsAt: now.Add(day), EndsAt: now.Add(8 * day)}
	active := db.Engagement{TenantID: 1, Name: "running", StartsAt: now.Add(-day), EndsAt: now.Add(7 * day)}
	for _, engagement := range []*db.Engagement{&future, &active} {
		if err := s.db.Create(engagement).Error; err != nil {
			t.Fatalf("seed engagement: %v", err)
		}
	}

	seed := func(id string, engagementID *uint) {
		if err := s.db.Create(&db.Implant{
			ID: id, Hostname: id, Status: "online", LastSeen: now, EngagementID: engagementID,
		}).Error; err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	seed("not-yet", &future.ID)
	seed("running", &active.ID)
	seed("unlinked", nil)

	// 1. A task before the window opens is refused, and the message says WHEN it opens.
	err := s.validateTaskCreationWithAgent(nil, "not-yet", "shell", "whoami", "", "", 0, nil)
	if err == nil {
		t.Fatal("an implant whose engagement has not started accepted a task")
	}
	if status, ok := clientErrorStatus(err); !ok || status != http.StatusForbidden {
		t.Fatalf("refusal maps to %d (mapped=%v), want 403: %v", status, ok, err)
	}
	if !containsFold(err.Error(), future.StartsAt.Format(time.RFC3339)) {
		t.Fatalf("the refusal does not name the start time: %v", err)
	}

	// 2. The refusal is audited under its own action, so "why was I blocked before the exercise" has an
	// answer that is not "expired".
	var started int64
	if err := s.db.Model(&db.AuditLog{}).Where("action = ?", "agent_not_started_block").Count(&started).Error; err != nil {
		t.Fatalf("count audit: %v", err)
	}
	if started != 1 {
		t.Fatalf("agent_not_started_block rows = %d, want 1", started)
	}
	var expired int64
	if err := s.db.Model(&db.AuditLog{}).Where("action = ?", "agent_expired_block").Count(&expired).Error; err != nil {
		t.Fatalf("count audit: %v", err)
	}
	if expired != 0 {
		t.Fatalf("a not-started refusal was recorded as expired (%d rows)", expired)
	}

	// 3. A running engagement and an unlinked implant both accept work.
	for _, agentID := range []string{"running", "unlinked"} {
		if err := s.validateTaskCreationWithAgent(nil, agentID, "shell", "whoami", "", "", 0, nil); err != nil {
			t.Fatalf("%s was refused: %v", agentID, err)
		}
	}

	// 4. Closing the future engagement does not make it startable: the window still opens when it was
	// agreed to. Closing early limits the end, it does not rewrite the beginning.
	if err := s.db.Model(&db.Engagement{}).Where("id = ?", future.ID).Update("closed_at", now).Error; err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := s.validateTaskCreationWithAgent(nil, "not-yet", "shell", "whoami", "", "", 0, nil); err == nil {
		t.Fatal("closing a not-yet-started engagement made it startable")
	}
}
