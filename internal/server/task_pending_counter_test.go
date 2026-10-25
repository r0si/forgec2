package server

import (
	"testing"
)

// TestTrackPendingTaskInitializesNilCounter pins the guard that keeps a
// map-less Server (tests, future constructors) from panicking on the first
// task insert. It also checks the cap still applies once the map exists.
func TestTrackPendingTaskInitializesNilCounter(t *testing.T) {
	s := &Server{}
	if err := s.trackPendingTask("agent-nil"); err != nil {
		t.Fatalf("first track on a nil map = %v", err)
	}
	if got := s.agentPendingTasks["agent-nil"]; got != 1 {
		t.Fatalf("count = %d, want 1 after the lazy init", got)
	}

	// Fill to the cap, then assert the next call is refused rather than
	// growing without bound.
	for i := 1; i < MaxPendingTasksPerAgent; i++ {
		if err := s.trackPendingTaskReserved("agent-nil", 0); err != nil {
			t.Fatalf("fill at %d: %v", i, err)
		}
	}
	if err := s.trackPendingTask("agent-nil"); err == nil {
		t.Fatal("cap was not enforced")
	}
	// The reserved headroom lets an abort through at the normal cap.
	if err := s.trackPendingTaskReserved("agent-nil", 1); err != nil {
		t.Fatalf("reserved slot refused at the cap: %v", err)
	}
}

// TestDecPendingTasksToleratesNilCounter covers the read path: decrementing a
// counter that was never initialized is a no-op, not a panic (the map is only
// written, so a nil map is safe here — this pins that it stays that way).
func TestDecPendingTasksToleratesNilCounter(t *testing.T) {
	s := &Server{}
	s.decPendingTasks("ghost")
	if len(s.agentPendingTasks) != 0 {
		t.Fatalf("decrement created entries: %+v", s.agentPendingTasks)
	}
}
