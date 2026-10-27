package plugin

import (
	"context"
	"testing"
	"time"
)

// TestExecutorCloneKeepsBudgets pins the setter invariants: changing one knob
// must not silently reset another, and a cloned executor shares the concurrency
// slot channel so the cap is global rather than per-configuration.
func TestExecutorCloneKeepsBudgets(t *testing.T) {
	base := newExecutor(3)
	base.cpuQuota = 5 * time.Second
	base.writeQuota = 1234

	// clone shares the slot channel.
	c := base.clone()
	if cap(c.slots) != cap(base.slots) {
		t.Fatalf("clone slots = %d, want %d", cap(c.slots), cap(base.slots))
	}
	if c.cpuQuota != base.cpuQuota || c.writeQuota != base.writeQuota {
		t.Fatalf("clone budgets = (%v, %d)", c.cpuQuota, c.writeQuota)
	}

	// Changing the concurrency cap keeps the budgets but swaps the channel.
	bigger := base.withMaxConcurrent(10)
	if cap(bigger.slots) != 10 {
		t.Fatalf("withMaxConcurrent slots = %d", cap(bigger.slots))
	}
	if bigger.cpuQuota != base.cpuQuota || bigger.writeQuota != base.writeQuota {
		t.Fatalf("withMaxConcurrent reset a budget: (%v, %d)", bigger.cpuQuota, bigger.writeQuota)
	}
	if bigger.slots == base.slots {
		t.Fatal("withMaxConcurrent kept the old slot channel")
	}
	// A non-positive cap falls back to the default.
	if got := cap(base.withMaxConcurrent(0).slots); got != defaultMaxConcurrentPlugins {
		t.Fatalf("withMaxConcurrent(0) = %d, want %d", got, defaultMaxConcurrentPlugins)
	}

	// Changing a budget keeps the cap and the slot channel.
	slower := base.withCPUQuota(11 * time.Second)
	if slower.cpuQuota != 11*time.Second {
		t.Fatalf("cpu quota = %v", slower.cpuQuota)
	}
	if cap(slower.slots) != cap(base.slots) || slower.slots != base.slots {
		t.Fatal("withCPUQuota did not share the slot channel")
	}
	if slower.writeQuota != base.writeQuota {
		t.Fatalf("withCPUQuota reset the write budget: %d", slower.writeQuota)
	}
	// A non-positive budget leaves the executor unchanged.
	if got := base.withCPUQuota(0); got != base {
		t.Fatal("withCPUQuota(0) returned a different executor")
	}
	if got := base.withWriteQuota(-1); got != base {
		t.Fatal("withWriteQuota(-1) returned a different executor")
	}

	// Nil-safe clone (the executor may be unset).
	var nilExec *executor
	if got := nilExec.clone(); got == nil || got.slots != nil {
		t.Fatalf("nil clone = %+v", got)
	}
}

// TestExecutorAcquireReleaseHonoursCancellation pins the backpressure contract:
// a saturated executor returns the context error instead of queueing plugin work
// without bound, and release frees exactly one slot.
func TestExecutorAcquireReleaseHonoursCancellation(t *testing.T) {
	e := newExecutor(1)
	ctx := context.Background()
	if err := e.acquire(ctx); err != nil {
		t.Fatalf("first acquire: %v", err)
	}

	// Saturated: a short deadline must fail rather than block forever.
	deadline, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if err := e.acquire(deadline); err == nil {
		t.Fatal("acquire on a saturated executor did not honour cancellation")
	}

	// Release one slot, then the next acquire succeeds.
	e.release()
	okCtx, okCancel := context.WithTimeout(ctx, time.Second)
	defer okCancel()
	if err := e.acquire(okCtx); err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	e.release()

	// An executor without a slot channel never blocks.
	noSlots := &executor{}
	if err := noSlots.acquire(ctx); err != nil {
		t.Fatalf("acquire without slots: %v", err)
	}
	noSlots.release() // must not panic
}
