package server

import (
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/db"
)

// TestEngagementWarnings covers the deadline reminder: without it, the first sign of an expiring
// engagement is a 403 at midnight, correctly refused but as a surprise.
//
// The properties that matter: warn inside the window and not outside it, never twice for the same
// deadline, re-arm when the deadline moves, and leave the audit trail.
func TestEngagementWarnings(t *testing.T) {
	s := newTasksTestServer(t)
	s.engagementWarns = newEngagementWarnTracker()

	now := time.Now()
	inAnHour := now.Add(time.Hour)
	inThreeDays := now.Add(72 * time.Hour)
	alreadyPast := now.Add(-time.Hour)

	seed := func(id string, expires *time.Time) {
		if err := s.db.Create(&db.Implant{
			ID: id, Hostname: id, Status: "online", LastSeen: now, ExpiresAt: expires,
		}).Error; err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	seed("soon", &inAnHour)
	seed("later", &inThreeDays) // outside the 24h window
	seed("gone", &alreadyPast)  // already expired: round 17 refuses its tasks, no warning needed
	seed("no-deadline", nil)    // unbounded

	// 1. Only the implant inside the window is announced.
	if got := s.announceEngagementWarnings(now); got != 1 {
		t.Fatalf("announced %d warnings, want 1", got)
	}

	var warnings []db.Notification
	if err := s.db.Where("type = ?", "engagement_expiring").Find(&warnings).Error; err != nil {
		t.Fatalf("read notifications: %v", err)
	}
	if len(warnings) != 1 {
		t.Fatalf("notifications = %d, want 1", len(warnings))
	}
	if warnings[0].AgentID != "soon" || warnings[0].Severity != "warning" {
		t.Fatalf("notification = %+v", warnings[0])
	}
	// The message has to say WHEN, otherwise it cannot be acted on.
	if !containsAllText(warnings[0].Message, "stops accepting tasks at") {
		t.Fatalf("message = %q", warnings[0].Message)
	}

	// 2. A second scan does not repeat it - one notification per implant per deadline.
	if got := s.announceEngagementWarnings(now.Add(time.Minute)); got != 0 {
		t.Fatalf("second scan announced %d more warnings, want 0", got)
	}

	// 3. Moving the deadline re-arms the reminder: the engagement was extended, and the new date is a
	// new fact the operator needs to hear about.
	extended := now.Add(20 * time.Hour)
	if err := s.db.Model(&db.Implant{}).Where("id = ?", "soon").Update("expires_at", extended).Error; err != nil {
		t.Fatalf("extend deadline: %v", err)
	}
	if got := s.announceEngagementWarnings(now.Add(2 * time.Minute)); got != 1 {
		t.Fatalf("after extending the deadline, announced %d warnings, want 1", got)
	}

	// 4. The trail says the warning happened, so "was anyone told" is answerable later.
	var audited int64
	if err := s.db.Model(&db.AuditLog{}).Where("action = ?", "engagement_expiring").Count(&audited).Error; err != nil {
		t.Fatalf("count audit: %v", err)
	}
	if audited != 2 {
		t.Fatalf("engagement_expiring audit rows = %d, want 2 (the initial warning and the extension)", audited)
	}
}

// TestEngagementWarningsRespectTheWindow pins the boundary: an implant exactly at the horizon is
// included, one second beyond it is not.
func TestEngagementWarningsRespectTheWindow(t *testing.T) {
	s := newTasksTestServer(t)
	now := time.Now().Add(-time.Second) // a little slack so the fixture cannot drift past the boundary

	atHorizon := now.Add(engagementWarnWindow)
	beyond := atHorizon.Add(2 * time.Second)

	for id, expires := range map[string]time.Time{"at-horizon": atHorizon, "beyond": beyond} {
		deadline := expires
		if err := s.db.Create(&db.Implant{
			ID: id, Hostname: id, Status: "online", LastSeen: now, ExpiresAt: &deadline,
		}).Error; err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}

	warnings, err := s.pendingEngagementWarnings(now, engagementWarnMaxEntries)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	var ids []string
	for _, warning := range warnings {
		ids = append(ids, warning.AgentID)
	}
	if len(ids) != 1 || ids[0] != "at-horizon" {
		t.Fatalf("window selected %v, want only at-horizon", ids)
	}
}

// containsAllText is a tiny helper so the assertion above reads as intent rather than as substring
// plumbing.
func containsAllText(haystack string, needles ...string) bool {
	for _, needle := range needles {
		if !containsFold(haystack, needle) {
			return false
		}
	}
	return true
}

func containsFold(haystack, needle string) bool {
	if needle == "" {
		return true
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
