package server

import (
	"log/slog"
	"math/rand"
	"strings"
	"time"

	"gorm.io/gorm"
)

// Beacon-path write contention policy. Under fleet check-in bursts concurrent
// writers contend, and the loser's write must ride out the contention instead
// of failing the beacon (which would stall delivery until the next check-in).
//
// On PostgreSQL that contention surfaces as a serialization/deadlock error
// (SQLSTATE 40001 / 40P01 — the direct analog of the SQLite-era SQLITE_BUSY) or
// as a transient connection failure when the pool is exhausted or the server is
// briefly unreachable. Both are retryable; everything else (constraint
// violations, bad SQL, a genuine logic bug) must return immediately so it is
// never masked by a retry loop.
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

// withBusyRetry runs fn, retrying transient PostgreSQL contention (serialization
// / deadlock and connection failures) with linear backoff + jitter. Any other
// error returns immediately. op names the call site for the DBBusyRetriesTotal
// metric ("claim", "result", "sweep", "enroll").
func (s *Server) withBusyRetry(op string, fn func() error) error {
	var err error
	for attempt := 1; attempt <= dbRetryAttempts; attempt++ {
		err = fn()
		if err == nil {
			return nil
		}
		if !isTransientDBError(err) {
			return err
		}
		if s.metrics != nil && s.metrics.DBBusyRetriesTotal != nil {
			s.metrics.DBBusyRetriesTotal.WithLabelValues(op).Inc()
		}
		if attempt == dbRetryAttempts {
			break
		}
		backoff := time.Duration(attempt)*dbRetryBaseBackoff + time.Duration(rand.Int63n(int64(dbRetryBaseBackoff)))
		slog.Warn("PostgreSQL write contention, retrying", "op", op, "attempt", attempt, "backoff_ms", backoff.Milliseconds(), "error", err)
		busyRetrySleep(backoff)
	}
	return err
}

// isTransientDBError reports whether err is a PostgreSQL failure worth retrying:
// a serialization/deadlock conflict (the write-contention analog of SQLITE_BUSY)
// or a transient connection problem (pool exhausted, server briefly down). It
// deliberately does NOT match constraint violations or SQL errors, which must
// surface on the first attempt.
func isTransientDBError(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	// Serialization / deadlock (SQLSTATE class 40): concurrent writers conflicted.
	if strings.Contains(s, "deadlock detected") ||
		strings.Contains(s, "could not serialize access") ||
		strings.Contains(s, "SQLSTATE 40001") ||
		strings.Contains(s, "SQLSTATE 40P01") {
		return true
	}
	// Connection-level transient failures.
	return strings.Contains(s, "connection refused") ||
		strings.Contains(s, "too many clients") ||
		strings.Contains(s, "database system is shutting down") ||
		strings.Contains(s, "connection reset")
}

// withBusyRetryDB is withBusyRetry for single gorm statements: it returns the
// last *gorm.DB so callers keep their .Error/.RowsAffected checks unchanged.
//
// Race note: a serialization conflict can (rarely) surface on COMMIT after the
// write landed. A retry then sees RowsAffected==0 and follows the existing
// duplicate path; the 5-minute reconcile heals any counter drift.
func (s *Server) withBusyRetryDB(op string, fn func() *gorm.DB) *gorm.DB {
	var res *gorm.DB
	_ = s.withBusyRetry(op, func() error {
		res = fn()
		return res.Error
	})
	return res
}
