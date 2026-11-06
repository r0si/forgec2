package server

import (
	"errors"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/testutil"
	promtestutil "github.com/prometheus/client_golang/prometheus/testutil"
)

// stubBusySleep replaces the retry backoff with a no-op so tests don't wait
// real time. It returns a counter of sleep calls (== retries performed).
func stubBusySleep(t *testing.T) *int {
	t.Helper()
	calls := 0
	old := busyRetrySleep
	busyRetrySleep = func(time.Duration) { calls++ }
	t.Cleanup(func() { busyRetrySleep = old })
	return &calls
}

func newRetryTestServer(t *testing.T) *Server {
	t.Helper()
	s := &Server{db: testutil.SetupTestDB(t), agentPendingTasks: make(map[string]int)}
	s.metrics = NewMetricsCollector(s)
	return s
}

func TestWithBusyRetrySuccessFirstTry(t *testing.T) {
	s := newRetryTestServer(t)
	sleeps := stubBusySleep(t)
	calls := 0
	err := s.withBusyRetry("test", func() error {
		calls++
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 1 || *sleeps != 0 {
		t.Fatalf("calls=%d sleeps=%d, want 1/0", calls, *sleeps)
	}
	if n := promtestutil.CollectAndCount(s.metrics.DBBusyRetriesTotal); n != 0 {
		t.Fatalf("metric samples=%d, want 0", n)
	}
}

func TestWithBusyRetryRecoversAfterLock(t *testing.T) {
	s := newRetryTestServer(t)
	sleeps := stubBusySleep(t)
	calls := 0
	err := s.withBusyRetry("test", func() error {
		calls++
		if calls < 3 {
			// PostgreSQL analog of SQLITE_BUSY: a serialization/deadlock
			// conflict from concurrent beacon writers. Transient — must retry.
			return errors.New("ERROR: deadlock detected (SQLSTATE 40P01)")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 3 || *sleeps != 2 {
		t.Fatalf("calls=%d sleeps=%d, want 3/2", calls, *sleeps)
	}
	if n := promtestutil.CollectAndCount(s.metrics.DBBusyRetriesTotal); n == 0 {
		t.Fatal("expected retry metric samples")
	}
}

func TestWithBusyRetryExhausts(t *testing.T) {
	s := newRetryTestServer(t)
	sleeps := stubBusySleep(t)
	calls := 0
	err := s.withBusyRetry("test", func() error {
		calls++
		return errors.New("could not serialize access due to concurrent update")
	})
	if err == nil {
		t.Fatal("expected the contention error back")
	}
	if calls != dbRetryAttempts || *sleeps != dbRetryAttempts-1 {
		t.Fatalf("calls=%d sleeps=%d, want %d/%d", calls, *sleeps, dbRetryAttempts, dbRetryAttempts-1)
	}
}

func TestWithBusyRetryNoRetryOnOtherErrors(t *testing.T) {
	s := &Server{}
	sleeps := stubBusySleep(t)
	calls := 0
	sentinel := errors.New("unique violation")
	err := s.withBusyRetry("test", func() error {
		calls++
		return sentinel
	})
	if err != sentinel {
		t.Fatalf("err=%v, want sentinel", err)
	}
	if calls != 1 || *sleeps != 0 {
		t.Fatalf("calls=%d sleeps=%d, want 1/0", calls, *sleeps)
	}
}

// TestIsTransientDBError pins exactly which errors the beacon write path
// retries: PostgreSQL contention (serialization/deadlock) and transient
// connection failures are retried, constraint and SQL errors are not, so a
// genuine bug is never masked by the retry loop.
func TestIsTransientDBError(t *testing.T) {
	retryable := []string{
		"ERROR: deadlock detected (SQLSTATE 40P01)",
		"could not serialize access due to concurrent update",
		"dial tcp 127.0.0.1:5432: connect: connection refused",
		"FATAL: sorry, too many clients already (SQLSTATE 53300)",
		"pq: connection reset by peer",
		"database system is shutting down",
		"SQLSTATE 40001 serialization_failure",
	}
	for _, msg := range retryable {
		if !isTransientDBError(errors.New(msg)) {
			t.Errorf("isTransientDBError(%q) = false, want true (should retry)", msg)
		}
	}
	notRetryable := []string{
		"ERROR: duplicate key value violates unique constraint",
		"ERROR: syntax error at or near \"SELEC\"",
		"ERROR: null value in column \"id\" violates not-null constraint",
		"no such table: implants",
	}
	for _, msg := range notRetryable {
		if isTransientDBError(errors.New(msg)) {
			t.Errorf("isTransientDBError(%q) = true, want false (must not retry)", msg)
		}
	}
	if isTransientDBError(nil) {
		t.Error("isTransientDBError(nil) = true, want false")
	}
}
