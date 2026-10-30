package server

import (
	"time"

	"github.com/forgec2/forgec2/internal/db"
)

// Build health for the readiness probe.
//
// Round 188 made /ready report whether the server CAN build; this reports whether it HAS been. A deployment whose every build fails is
// broken in the way an operator discovers only when they need a payload, and /ready answered "ok" through all of it.
//
// The two rules that keep this from becoming noise:
//   - The check is informational unless the situation is unambiguous: a warning needs at least three builds in the window AND no success,
//     which is "the toolchain is broken", not "somebody made a typo once".
//   - It is cached, because /ready polls every few seconds and this reads the build history.

const (
	buildHealthWindow   = 24 * time.Hour
	buildHealthCacheTTL = 2 * time.Minute
	// buildHealthWarnMinimum is the sample size below which a run of failures is not evidence of anything.
	buildHealthWarnMinimum = 3
	// buildHealthScan bounds the read: the classification needs the error text, so it cannot be an aggregate query.
	buildHealthScan = 200
)

// buildHealthSnapshot is what /ready reports, and whether a warning is warranted.
type buildHealthSnapshot struct {
	Text    string
	Warn    bool
	Scanned bool
}

// buildHealth returns the cached snapshot, refreshing it when stale.
func (s *Server) buildHealth() buildHealthSnapshot {
	s.buildHealthMu.Lock()
	cached, at := s.buildHealthCached, s.buildHealthAt
	s.buildHealthMu.Unlock()
	if !at.IsZero() && time.Since(at) < buildHealthCacheTTL {
		return cached
	}

	snapshot := buildHealthSnapshot{Scanned: true}
	if s.db == nil {
		return snapshot
	}
	since := time.Now().Add(-buildHealthWindow)
	var rows []db.BuildLog
	if err := s.db.WithContext(s.ctx).
		Where("created_at >= ?", since).
		Order("created_at desc").
		Limit(buildHealthScan).
		Find(&rows).Error; err != nil {
		// A failed read is not a build problem: say the check could not run rather than implying the toolchain is fine.
		snapshot.Text = "could not read the build history"
		snapshot.Scanned = false
	} else {
		failed, succeeded := 0, 0
		for _, row := range rows {
			if row.Status == "failed" {
				failed++
			} else if row.Status == "success" {
				succeeded++
			}
		}
		switch {
		case len(rows) == 0:
			snapshot.Text = "no builds in the last 24h"
		case failed == 0:
			snapshot.Text = "ok: " + itoaInt(succeeded) + " build(s), none failed in 24h"
		case succeeded == 0 && failed >= buildHealthWarnMinimum:
			snapshot.Text = "every one of the last " + itoaInt(failed) + " build(s) failed in 24h"
			snapshot.Warn = true
		default:
			snapshot.Text = itoaInt(failed) + " of " + itoaInt(len(rows)) + " build(s) failed in 24h"
		}
	}

	s.buildHealthMu.Lock()
	s.buildHealthCached = snapshot
	s.buildHealthAt = time.Now()
	s.buildHealthMu.Unlock()
	return snapshot
}

// resetBuildHealthCache drops the cached snapshot. Tests use it so one test's build logs cannot decide another's readiness answer.
func (s *Server) resetBuildHealthCache() {
	s.buildHealthMu.Lock()
	defer s.buildHealthMu.Unlock()
	s.buildHealthCached = buildHealthSnapshot{}
	s.buildHealthAt = time.Time{}
}
