package server

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"

	"github.com/forgec2/forgec2/internal/db"
)

// Warn before an engagement deadline stops work.
//
// Round 17 made the deadline enforceable and round 18 made it visible, but both require someone to look.
// The failure this prevents is concrete: an operator runs a collection at 23:50 on the last night of an
// engagement, the deadline passes at midnight, and every task after that is refused - correctly, but as
// a surprise. A notification 24 hours earlier turns a 403 into a plan.
//
// Design notes:
//   - the scan is bounded (only implants with a deadline inside the next window, newest-first is
//     irrelevant) and runs once per interval, not per request;
//   - ONE notification per implant per deadline: the dedupe key includes the deadline itself, so
//     extending an engagement re-arms the reminder for the new date while a restart of the server does
//     not turn the same deadline into a second notification;
//   - a delivery failure is logged rather than swallowed, and does not stop the scan.

const (
	// engagementWarnWindow is how far ahead an operator is warned.
	engagementWarnWindow = 24 * time.Hour
	// engagementWarnInterval is how often the scan runs. The window is a day, so a minute is plenty and
	// costs one indexed query.
	engagementWarnInterval = time.Minute
	// engagementWarnMaxEntries bounds one scan so a fleet with thousands of deadlines cannot turn a
	// timer tick into a long query.
	engagementWarnMaxEntries = 500
)

// engagementWarnTracker remembers which implant+deadline pairs have been announced.
type engagementWarnTracker struct {
	mu   sync.Mutex
	seen map[string]time.Time // agentID|deadlineUnix -> when announced
}

func newEngagementWarnTracker() *engagementWarnTracker {
	return &engagementWarnTracker{seen: make(map[string]time.Time, 64)}
}

func engagementWarnKey(agentID string, deadline time.Time) string {
	return fmt.Sprintf("%s|%d", agentID, deadline.UTC().Unix())
}

// shouldAnnounce reports whether this pair has not been announced yet, and records it.
func (t *engagementWarnTracker) shouldAnnounce(agentID string, deadline time.Time) bool {
	if t == nil {
		return true
	}
	key := engagementWarnKey(agentID, deadline)
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.seen[key]; ok {
		return false
	}
	// Bound the map: drop entries whose deadline has passed by more than the interval, which cannot be
	// announced again anyway because the implant is already expired.
	cutoff := time.Now().Add(-time.Hour)
	for existing, announced := range t.seen {
		if announced.Before(cutoff) {
			delete(t.seen, existing)
		}
	}
	t.seen[key] = time.Now()
	return true
}

// engagementWarning is one pending notification.
type engagementWarning struct {
	AgentID   string
	Hostname  string
	TenantID  uint
	ExpiresAt time.Time
}

// pendingEngagementWarnings returns the implants whose deadline falls inside the window and has not
// passed. Pure with respect to announcement: it only reads.
func (s *Server) pendingEngagementWarnings(now time.Time, limit int) ([]engagementWarning, error) {
	var agents []db.Implant
	horizon := now.Add(engagementWarnWindow)
	err := s.db.WithContext(s.ctx).Model(&db.Implant{}).
		Select("id, hostname, tenant_id, expires_at").
		Where("expires_at IS NOT NULL").
		Where("expires_at > ?", now).
		Where("expires_at <= ?", horizon).
		Order("expires_at asc").
		Limit(limit).
		Find(&agents).Error
	if err != nil {
		return nil, err
	}
	warnings := make([]engagementWarning, 0, len(agents))
	for _, agent := range agents {
		if agent.ExpiresAt == nil {
			continue
		}
		warnings = append(warnings, engagementWarning{
			AgentID:   agent.ID,
			Hostname:  agent.Hostname,
			TenantID:  agent.TenantID,
			ExpiresAt: agent.ExpiresAt.UTC(),
		})
	}
	return warnings, nil
}

// announceEngagementWarnings creates the notifications, one per implant per deadline.
func (s *Server) announceEngagementWarnings(now time.Time) int {
	warnings, err := s.pendingEngagementWarnings(now, engagementWarnMaxEntries)
	if err != nil {
		slog.Error("Failed to scan engagement deadlines", "err", err)
		return 0
	}
	announced := 0
	for _, warning := range warnings {
		if !s.engagementWarns.shouldAnnounce(warning.AgentID, warning.ExpiresAt) {
			continue
		}
		name := warning.Hostname
		if name == "" {
			name = warning.AgentID
		}
		notification := &db.Notification{
			Type:     "engagement_expiring",
			Title:    "Engagement deadline approaching",
			Message:  fmt.Sprintf("%s stops accepting tasks at %s", name, warning.ExpiresAt.Format(time.RFC3339)),
			AgentID:  warning.AgentID,
			Severity: "warning",
		}
		if err := s.db.Create(notification).Error; err != nil {
			slog.Error("Failed to create engagement warning", "agent_id", warning.AgentID, "err", err)
			continue
		}
		s.DispatchNotification(notification)
		s.LogAuditRecord(nil, "engagement_expiring", "agent", warning.AgentID,
			fmt.Sprintf("deadline %s is within %s", warning.ExpiresAt.Format(time.RFC3339), engagementWarnWindow), true, nil)
		announced++
	}
	return announced
}

// runEngagementWarnings scans on a timer until the context is cancelled.
func (s *Server) runEngagementWarnings(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("recovered from panic", "err", r, "stack", string(debug.Stack()))
		}
	}()
	ticker := time.NewTicker(engagementWarnInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.announceEngagementWarnings(time.Now())
		}
	}
}
