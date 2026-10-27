package server

import (
	"context"
	"testing"
	"time"
)

// TestShutdownDrainsTrackedWorkers pins the shutdown contract: a worker registered
// on s.wg is waited for, and cancellation reaches it through s.ctx. Without this,
// Shutdown could close the database while a worker was still writing to it.
func TestShutdownDrainsTrackedWorkers(t *testing.T) {
	s := mustTenantServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	s.ctx = ctx
	s.ctxCancel = cancel

	started := make(chan struct{})
	finished := make(chan struct{})
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		close(started)
		<-s.ctx.Done()
		close(finished)
	}()
	<-started

	done := make(chan struct{})
	go func() {
		s.Shutdown()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Shutdown did not return")
	}
	select {
	case <-finished:
	default:
		t.Fatal("Shutdown returned but the tracked worker was still running")
	}

	// Shutdown is idempotent: a second call (and a second goroutine doing it) must
	// not panic or block.
	s.Shutdown()
}

// TestShutdownOnBareServerDoesNotPanic covers the degraded shape: a Server built
// without Run (no context, no listeners) must survive Shutdown instead of
// dereferencing a nil context.
func TestShutdownOnBareServerDoesNotPanic(t *testing.T) {
	s := &Server{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("Shutdown panicked on a bare server: %v", r)
			}
		}()
		s.Shutdown()
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Shutdown hung on a bare server")
	}
}
