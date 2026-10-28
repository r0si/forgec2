package server

import (
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/db"
)

// TestEngagementWindowWarnings covers the engagement-level reminders, which the per-implant warning
// cannot replace: a whole fleet stops when the exercise ends, so "the window closes in a day" and "the
// window has closed, produce the summary" are the two facts worth pushing.
func TestEngagementWindowWarnings(t *testing.T) {
	s := newTasksTestServer(t)
	s.engagementWarns = newEngagementWarnTracker()

	now := time.Now().UTC().Truncate(time.Second)
	day := 24 * time.Hour

	soon := db.Engagement{TenantID: 1, Name: "closing tomorrow", StartsAt: now.Add(-5 * day), EndsAt: now.Add(12 * time.Hour)}
	far := db.Engagement{TenantID: 1, Name: "closing next month", StartsAt: now.Add(-day), EndsAt: now.Add(30 * day)}
	ended := db.Engagement{TenantID: 1, Name: "already over", StartsAt: now.Add(-10 * day), EndsAt: now.Add(-2 * day)}
	closed := db.Engagement{TenantID: 1, Name: "closed early", StartsAt: now.Add(-5 * day), EndsAt: now.Add(6 * day), ClosedAt: ptrTime(now.Add(-day))}
	for _, engagement := range []*db.Engagement{&soon, &far, &ended, &closed} {
		if err := s.db.Create(engagement).Error; err != nil {
			t.Fatalf("seed engagement: %v", err)
		}
	}

	// 1. One "closing soon" and one "has ended". The far-out window and the early-closed one say nothing.
	if got := s.announceEngagementWindowWarnings(now); got != 2 {
		t.Fatalf("announced %d window warnings, want 2 (one closing, one ended)", got)
	}

	var ending, over []db.Notification
	if err := s.db.Where("type = ?", "engagement_ending").Find(&ending).Error; err != nil {
		t.Fatalf("read ending: %v", err)
	}
	if err := s.db.Where("type = ?", "engagement_ended").Find(&over).Error; err != nil {
		t.Fatalf("read ended: %v", err)
	}
	if len(ending) != 1 || len(over) != 1 {
		t.Fatalf("notifications = %d ending / %d ended, want 1 each", len(ending), len(over))
	}
	// The message has to name the window and the exercise, otherwise it cannot be acted on.
	if !containsFold(ending[0].Message, "closing tomorrow") || !containsFold(ending[0].Message, "ends at") {
		t.Fatalf("ending message = %q", ending[0].Message)
	}
	if !containsFold(over[0].Message, "already over") {
		t.Fatalf("ended message = %q", over[0].Message)
	}

	// 2. A second pass repeats nothing.
	if got := s.announceEngagementWindowWarnings(now.Add(time.Minute)); got != 0 {
		t.Fatalf("second pass announced %d, want 0", got)
	}

	// 3. Extending a window re-arms the reminder: the new end is a new fact.
	extended := now.Add(20 * time.Hour)
	if err := s.db.Model(&db.Engagement{}).Where("id = ?", soon.ID).Update("ends_at", extended).Error; err != nil {
		t.Fatalf("extend: %v", err)
	}
	if got := s.announceEngagementWindowWarnings(now.Add(2 * time.Minute)); got != 1 {
		t.Fatalf("after extending the window, announced %d, want 1", got)
	}

	// 4. Both decisions are audited.
	var endingAudit, endedAudit int64
	if err := s.db.Model(&db.AuditLog{}).Where("action = ?", "engagement_ending").Count(&endingAudit).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if err := s.db.Model(&db.AuditLog{}).Where("action = ?", "engagement_ended").Count(&endedAudit).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if endingAudit != 2 || endedAudit != 1 {
		t.Fatalf("audit rows = %d ending / %d ended, want 2 / 1 (initial + extension, one end)", endingAudit, endedAudit)
	}

	// 5. A closed engagement never produces either notice, even if its agreed end passes later.
	if err := s.db.Model(&db.Engagement{}).Where("id = ?", closed.ID).Update("closed_at", now.Add(-time.Minute)).Error; err != nil {
		t.Fatalf("reclose: %v", err)
	}
	// The extended "soon" window has now passed, so it legitimately produces its one ended notice; what
	// must NOT happen is a notice for the early-closed engagement. My first expectation demanded zero
	// total and was therefore wrong - the assertion is now about the engagement, not the count.
	s.announceEngagementWindowWarnings(now.Add(7 * day))
	var all []db.Notification
	if err := s.db.Where("type IN ?", []string{"engagement_ending", "engagement_ended"}).Find(&all).Error; err != nil {
		t.Fatalf("read notifications: %v", err)
	}
	for _, notification := range all {
		if containsFold(notification.Message, "closed early") {
			t.Fatalf("a closed engagement produced a notice: %q", notification.Message)
		}
	}
}
