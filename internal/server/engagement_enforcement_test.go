package server

import (
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/db"
)

// TestEffectiveDeadlineMergesEngagementAndImplant covers the merge that makes engagements matter: the
// engagement is the promise made to the customer, so an implant built to run for a month must still stop
// when the exercise it belongs to ends - or sooner, whichever comes first.
func TestEffectiveDeadlineMergesEngagementAndImplant(t *testing.T) {
	s := newTasksTestServer(t)
	// Truncated so the stored value and the expectation agree exactly: SQLite keeps nanoseconds, and a
	// monotonic reading from time.Now would never compare equal to a round-tripped timestamp.
	now := time.Now().UTC().Truncate(time.Second)

	// An engagement ending soon, one ending late, and one closed early.
	soonEnd := now.Add(2 * time.Hour)
	lateEnd := now.Add(30 * 24 * time.Hour)
	closedAt := now.Add(-time.Hour)

	openSoon := db.Engagement{TenantID: 1, Name: "short", StartsAt: now.Add(-24 * time.Hour), EndsAt: soonEnd}
	openLate := db.Engagement{TenantID: 1, Name: "long", StartsAt: now.Add(-24 * time.Hour), EndsAt: lateEnd}
	closed := db.Engagement{TenantID: 1, Name: "closed", StartsAt: now.Add(-48 * time.Hour), EndsAt: lateEnd, ClosedAt: &closedAt}
	for _, engagement := range []*db.Engagement{&openSoon, &openLate, &closed} {
		if err := s.db.Create(engagement).Error; err != nil {
			t.Fatalf("seed engagement: %v", err)
		}
	}

	implantExpiry := now.Add(7 * 24 * time.Hour)
	seed := func(id string, expiry *time.Time, engagementID *uint) db.Implant {
		agent := db.Implant{
			ID: id, Hostname: id, Status: "online", LastSeen: now,
			ExpiresAt: expiry, EngagementID: engagementID,
		}
		if err := s.db.Create(&agent).Error; err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
		return agent
	}

	// 1. Engagement ends sooner than the implant: the engagement wins.
	shortAgent := seed("short-agent", &implantExpiry, &openSoon.ID)
	deadline := s.effectiveAgentDeadline(shortAgent)
	if deadline == nil || !deadline.Equal(soonEnd.Truncate(time.Second)) {
		t.Fatalf("effective deadline = %v, want the engagement end %v", deadline, soonEnd)
	}

	// 2. Engagement ends later than the implant: the implant wins.
	longAgent := seed("long-agent", &implantExpiry, &openLate.ID)
	if got := s.effectiveAgentDeadline(longAgent); got == nil || !got.Equal(implantExpiry.Truncate(time.Second)) {
		t.Fatalf("effective deadline = %v, want the implant expiry %v", got, implantExpiry)
	}

	// 3. A closed engagement ends at ClosedAt, not at its agreed EndsAt: closing early is a decision and
	// the earlier end is the one that applies.
	closedAgent := seed("closed-agent", nil, &closed.ID)
	got := s.effectiveAgentDeadline(closedAgent)
	if got == nil || !got.Equal(closedAt.Truncate(time.Second)) {
		t.Fatalf("effective deadline = %v, want the close time %v", got, closedAt)
	}
	// ...which means the agent is already past its deadline and refuses work, despite having no implant
	// deadline of its own.
	if !s.agentExpired(closedAgent) {
		t.Fatal("an agent in a closed engagement is not considered expired")
	}
	if err := s.validateTaskCreationWithAgent(nil, "closed-agent", "shell", "whoami", "", "", 0, nil); err == nil {
		t.Fatal("an agent in a closed engagement accepted a task")
	}

	// 4. No engagement: the implant deadline alone applies, exactly as before this feature.
	plain := seed("plain-agent", &implantExpiry, nil)
	if got := s.effectiveAgentDeadline(plain); got == nil || !got.Equal(implantExpiry.Truncate(time.Second)) {
		t.Fatalf("effective deadline = %v, want the implant expiry", got)
	}

	// 5. An engagement that no longer exists must not block work: the implant deadline still applies.
	ghostID := uint(999999)
	ghost := seed("ghost-agent", &implantExpiry, &ghostID)
	if got := s.effectiveAgentDeadline(ghost); got == nil || !got.Equal(implantExpiry.Truncate(time.Second)) {
		t.Fatalf("a missing engagement changed the deadline to %v", got)
	}

	// 6. The refusal names the deadline that actually stopped the work.
	if err := s.validateTaskCreationWithAgent(nil, "short-agent", "shell", "whoami", "", "", 0, nil); err != nil {
		t.Fatalf("an agent whose engagement has not ended was refused: %v", err)
	}
}

// TestRegSecretCarriesEngagement proves the scope travels with the build: one secret carries both the
// deadline and the engagement, and an implant created from it inherits both.
func TestRegSecretCarriesEngagement(t *testing.T) {
	s := newTasksTestServer(t)
	now := time.Now().UTC().Truncate(time.Second)
	expiry := now.Add(72 * time.Hour)

	engagement := db.Engagement{TenantID: 1, Name: "carried", StartsAt: now, EndsAt: now.Add(7 * 24 * time.Hour)}
	if err := s.db.Create(&engagement).Error; err != nil {
		t.Fatalf("seed engagement: %v", err)
	}
	if err := s.db.Create(&db.RegSecret{
		ID: "secret-carried", ExpiresAt: &expiry, EngagementID: &engagement.ID,
	}).Error; err != nil {
		t.Fatalf("seed secret: %v", err)
	}

	gotExpiry, gotEngagement := s.regSecretBounds("secret-carried")
	if gotExpiry == nil || !gotExpiry.Equal(expiry.Truncate(time.Second)) {
		t.Fatalf("expiry = %v, want %v", gotExpiry, expiry)
	}
	if gotEngagement == nil || *gotEngagement != engagement.ID {
		t.Fatalf("engagement = %v, want %d", gotEngagement, engagement.ID)
	}

	// The registration path stores both on the new implant row.
	s.ensureBeaconImplantRow("carried-agent", "secret-carried")
	var stored db.Implant
	if err := s.db.Where("id = ?", "carried-agent").First(&stored).Error; err != nil {
		t.Fatalf("reload implant: %v", err)
	}
	if stored.EngagementID == nil || *stored.EngagementID != engagement.ID {
		t.Fatalf("implant engagement = %v, want %d", stored.EngagementID, engagement.ID)
	}
	if stored.ExpiresAt == nil || !stored.ExpiresAt.Equal(expiry.Truncate(time.Second)) {
		t.Fatalf("implant expiry = %v, want %v", stored.ExpiresAt, expiry)
	}

	// A secret without scope keeps producing unbounded implants.
	if err := s.db.Create(&db.RegSecret{ID: "secret-plain"}).Error; err != nil {
		t.Fatalf("seed plain secret: %v", err)
	}
	plainExpiry, plainEngagement := s.regSecretBounds("secret-plain")
	if plainExpiry != nil || plainEngagement != nil {
		t.Fatalf("a plain secret produced scope: %v %v", plainExpiry, plainEngagement)
	}
}
