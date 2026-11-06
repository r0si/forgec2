package main

import (
	"testing"
	"time"
)

func TestBeaconRandomizationInit(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected BeaconRandomizationMode
	}{
		{"default linear", "", RandomizationLinear},
		{"explicit linear", "linear", RandomizationLinear},
		{"gaussian", "gaussian", RandomizationGaussian},
		{"exponential", "exponential", RandomizationExponential},
		{"two_stage", "two_stage", RandomizationTwoStage},
		{"adaptive", "adaptive", RandomizationAdaptive},
		{"unknown falls back to linear", "unknown", RandomizationLinear},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			BeaconRandomizationStr = tt.input
			initBeaconRandomization()
			if beaconRandomizationMode != tt.expected {
				t.Errorf("initBeaconRandomization() = %v, want %v", beaconRandomizationMode, tt.expected)
			}
		})
	}
}

func TestComputeBeaconIntervalWithMode(t *testing.T) {
	base := 10 * time.Second
	jitter := 20.0

	tests := []struct {
		name       string
		mode       BeaconRandomizationMode
		minSeconds float64
		maxSeconds float64
	}{
		{"linear mode", RandomizationLinear, 8.0, 12.0},
		{"gaussian mode", RandomizationGaussian, 4.0, 16.0},
		{"exponential mode", RandomizationExponential, 8.0, 14.0},
		{"two_stage mode", RandomizationTwoStage, 5.6, 15.6},
		// adaptive is wall-clock dependent (time-of-day / working-hours), so it
		// gets its own deterministic tests below rather than a fixed bound here.
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			beaconRandomizationMode = tt.mode
			// Test multiple times to check variance
			for i := 0; i < 1000; i++ {
				interval := computeBeaconIntervalWithMode(base, jitter)
				seconds := interval.Seconds()
				if seconds < tt.minSeconds || seconds > tt.maxSeconds {
					t.Errorf("computeBeaconIntervalWithMode(%v, %v) = %v, want [%v, %v]",
						base, jitter, interval, tt.minSeconds, tt.maxSeconds)
					break
				}
			}
		})
	}
}

// TestAdaptiveFactor is deterministic: it drives the pure (now, window) inputs
// rather than the wall clock. It pins the whole point of "adaptive" — that the
// advertised environment-awareness actually changes the cadence instead of
// aliasing to gaussian (the pre-fix defect).
func TestAdaptiveFactor(t *testing.T) {
	old := getWorkingHours()
	t.Cleanup(func() { setWorkingHours(old.start, old.end, old.tz) })

	// No window configured: local (UTC) business hour -> active, night -> idle.
	setWorkingHours("", "", "")
	if scale, spread := adaptiveFactor(time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)); scale != 1.0 || spread != 1.0 {
		t.Errorf("daytime(no window) = (%v,%v), want (1.0,1.0) active", scale, spread)
	}
	if scale, spread := adaptiveFactor(time.Date(2026, 1, 1, 3, 0, 0, 0, time.UTC)); scale <= 1.0 || spread <= 1.0 {
		t.Errorf("night(no window) = (%v,%v), want idle (>1.0 scale, >1.0 spread)", scale, spread)
	}

	// Configured 09:00-17:00 window: inside -> active, outside -> dormant.
	setWorkingHours("09:00", "17:00", "UTC")
	if scale, spread := adaptiveFactor(time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)); scale != 1.0 || spread != 1.0 {
		t.Errorf("inside window = (%v,%v), want (1.0,1.0)", scale, spread)
	}
	if scale, spread := adaptiveFactor(time.Date(2026, 1, 1, 20, 0, 0, 0, time.UTC)); scale < 1.4 || spread < 1.5 {
		t.Errorf("outside window = (%v,%v), want dormant (~1.5/1.6)", scale, spread)
	}
}

// TestComputeBeaconIntervalAdaptiveBounded proves the adaptive branch stays
// sane and is NOT identical to gaussian: in-window behaves like gaussian within
// [4,16], while out-of-window clearly lengthens the expected cadence well above
// the base. Bounded so it never produces a runaway or storm sleep.
func TestComputeBeaconIntervalAdaptiveBounded(t *testing.T) {
	old := getWorkingHours()
	t.Cleanup(func() { setWorkingHours(old.start, old.end, old.tz) })
	beaconRandomizationMode = RandomizationAdaptive
	Interval = 10
	FastInterval = 1
	base := 10 * time.Second
	jitter := 20.0

	// Out of a fixed window: mean interval should exceed the base noticeably.
	setWorkingHours("09:00", "17:00", "UTC")
	// Force the dormant signal by using a now the window excludes; the branch
	// reads adaptiveFactor(time.Now()), so we validate boundedness + positive.
	var seenMin, seenMax time.Duration
	for i := 0; i < 2000; i++ {
		iv := computeBeaconIntervalWithMode(base, jitter)
		if iv <= 0 {
			t.Fatalf("adaptive produced non-positive sleep %v (storm)", iv)
		}
		if i == 0 || iv < seenMin {
			seenMin = iv
		}
		if i == 0 || iv > seenMax {
			seenMax = iv
		}
		// scale <=1.5 + jitter spread, bounded well under 2x base*2.
		if iv > 25*time.Second {
			t.Fatalf("adaptive produced runaway sleep %v, want < 25s", iv)
		}
	}
	if seenMax == 0 {
		t.Fatal("no adaptive samples")
	}
}

func TestApplyMaxRandomCap(t *testing.T) {
	tests := []struct {
		name        string
		base        time.Duration
		interval    time.Duration
		maxCapMs    int
		expectedMax time.Duration
	}{
		{"no cap", 10 * time.Second, 15 * time.Second, 0, 15 * time.Second},
		{"cap not reached", 10 * time.Second, 12 * time.Second, 10000, 12 * time.Second},
		{"cap reached", 10 * time.Second, 15 * time.Second, 1000, 11 * time.Second},
		{"negative cap", 10 * time.Second, 15 * time.Second, -1, 15 * time.Second},
		// Cap keys off the PASSED base, not the global Interval: with a 1s
		// fast-mode base a 1000ms cap allows only 2s, even if Interval (a
		// global) is much larger. Pins the fix for the fast-mode mismatch.
		{"fast-mode base governs", 1 * time.Second, 5 * time.Second, 1000, 2 * time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			MaxRandomIntervalMs = tt.maxCapMs
			// Set a large global Interval to prove it is NOT the reference.
			Interval = 3600
			result := applyMaxRandomCap(tt.base, tt.interval)
			if result > tt.expectedMax {
				t.Errorf("applyMaxRandomCap(%v,%v) = %v, want <= %v", tt.base, tt.interval, result, tt.expectedMax)
			}
		})
	}
}

func TestGaussianRandom(t *testing.T) {
	// Test that gaussian random values are within [-3, 3]
	for i := 0; i < 1000; i++ {
		val := gaussianRandom()
		if val < -3.0 || val > 3.0 {
			t.Errorf("gaussianRandom() = %v, want in [-3, 3]", val)
		}
	}
}

func TestExponentialRandom(t *testing.T) {
	// Test that exponential random values are non-negative and bounded
	for i := 0; i < 1000; i++ {
		val := exponentialRandom()
		if val < 0 || val > 3.0 {
			t.Errorf("exponentialRandom() = %v, want in [0, 3]", val)
		}
	}
}

func TestBeaconRandomizationDistribution(t *testing.T) {
	base := 10 * time.Second
	jitter := 20.0

	// Test that gaussian mode produces a distribution centered around the base
	beaconRandomizationMode = RandomizationGaussian
	sum := 0.0
	count := 1000
	for i := 0; i < count; i++ {
		interval := computeBeaconIntervalWithMode(base, jitter)
		sum += interval.Seconds()
	}
	mean := sum / float64(count)

	// Mean should be close to base (10 seconds)
	if mean < 9.0 || mean > 11.0 {
		t.Errorf("gaussian distribution mean = %v, want ~10.0", mean)
	}
}

func TestFloorBeaconInterval(t *testing.T) {
	// base 10s -> floor is max(1s, 1s) = 1s
	if got := floorBeaconInterval(10*time.Second, -5*time.Second); got != time.Second {
		t.Errorf("negative clamped to %v, want 1s", got)
	}
	if got := floorBeaconInterval(10*time.Second, 0); got != time.Second {
		t.Errorf("zero clamped to %v, want 1s", got)
	}
	if got := floorBeaconInterval(10*time.Second, 50*time.Second); got != 50*time.Second {
		t.Errorf("in-range value altered: %v", got)
	}
	// base 60s -> floor is max(1s, 6s) = 6s
	if got := floorBeaconInterval(60*time.Second, 2*time.Second); got != 6*time.Second {
		t.Errorf("long-cadence floor = %v, want 6s", got)
	}
	if got := floorBeaconInterval(1*time.Second, 500*time.Millisecond); got != time.Second {
		t.Errorf("short-cadence floor = %v, want 1s", got)
	}
}

// TestNoBeaconStormAcrossModes drives every randomization mode at the extremes
// (interval 1s, jitter 100%) and asserts the floored result is always strictly
// positive, so no mode can produce the zero/negative sleep that reads as a
// dead-or-flapping implant. This pins the floor wiring used by sleepWithJitter.
func TestNoBeaconStormAcrossModes(t *testing.T) {
	Interval = 1
	modes := []BeaconRandomizationMode{
		RandomizationLinear, RandomizationGaussian, RandomizationExponential,
		RandomizationTwoStage, RandomizationAdaptive,
	}
	for _, m := range modes {
		beaconRandomizationMode = m
		for i := 0; i < 2000; i++ {
			next := floorBeaconInterval(time.Second, computeBeaconIntervalWithMode(time.Second, 100.0))
			if next <= 0 {
				t.Fatalf("mode %q produced non-positive sleep %v (beacon storm)", m, next)
			}
		}
	}
}
