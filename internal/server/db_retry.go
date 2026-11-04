package server

import (
	"log/slog"
	"math/rand"
	"strings"
	"time"

	"gorm.io/gorm"
)

// Beacon-path write contention policy. SQLite runs MaxOpenConns=1, so every
// beacon claim/result/sweep serializes on one writer: under fleet check-in
// bursts the loser gets SQLITE_BUSY ("database is locked"). Failing the
// beacon then stalls delivery until the next check-in; a short bounded retry
// rides out the burst instead.
//
// Attempts and backoff mirror the one existing retry (ai_runs: 50ms) and stay
// far below beacon intervals so a wedged writer still surfaces fast.

// dbRetryAttempts is the total tries per write (1 initial + 2 retries).
const dbRetryAttempts = 3

// dbRetryBaseBackoff scales the linear backoff (attempt × base + jitter).
const dbRetryBaseBackoff = 50 * time.Millisecond

// busyRetrySleep sleeps between attempts. Overridden in tests to avoid
// real-time waits.
var busyRetrySleep = time.Sleep

// withBusyRetry runs fn, retrying PostgreSQL connection failures with
// linear backoff + jitter. Non-lock errors return immediately. op names the
// call site for the DBBusyRetriesTotal metric ("claim", "result", "sweep",
// "enroll").
func (s *Server) withBusyRetry(op string, fn func() error) error {
	var err error
	for attempt := 1; attempt <= dbRetryAttempts; attempt++ {
		err = fn()
		if err == nil {
			return nil
		}
		if !isPostgresConnectionError(err) {
			return err
		}
		if s.metrics != nil && s.metrics.DBBusyRetriesTotal != nil {
			s.metrics.DBBusyRetriesTotal.WithLabelValues(op).Inc()
		}
		if attempt == dbRetryAttempts {
			break
		}
		backoff := time.Duration(attempt)*dbRetryBaseBackoff + time.Duration(rand.Int63n(int64(dbRetryBaseBackoff)))
		slog.Warn("PostgreSQL connection busy, retrying write", "op", op, "attempt", attempt, "backoff_ms", backoff.Milliseconds())
		busyRetrySleep(backoff)
	}
	return err
}

// isPostgresConnectionError reports whether err is a PostgreSQL connection
// failure worth retrying (connection refused, too many clients, etc.).
func isPostgresConnectionError(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "connection refused") ||
		strings.Contains(s, "too many clients") ||
		strings.Contains(s, "database system is shutting down") ||
		strings.Contains(s, "connection reset")
}

// withBusyRetryDB is withBusyRetry for single gorm statements: it returns the
// last *gorm.DB so callers keep their .Error/.RowsAffected checks unchanged.
//
// Race note: SQLITE_BUSY can (rarely) surface on COMMIT after the write
// landed. A retry then sees RowsAffected==0 and follows the existing
// duplicate path; the 5-minute reconcile heals any counter drift.
func (s *Server) withBusyRetryDB(op string, fn func() *gorm.DB) *gorm.DB {
	var res *gorm.DB
	_ = s.withBusyRetry(op, func() error {
		res = fn()
		return res.Error
	})
	return res
}
