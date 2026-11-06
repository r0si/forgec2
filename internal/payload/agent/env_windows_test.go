//go:build windows

package main

import (
	"testing"
	"time"
)

func TestHighValueDefaultAllowsShell(t *testing.T) {
	p := defaultProfiles[EnvHighValue]
	if !p.AllowShell {
		t.Fatal("high_value default profile must allow interactive shell")
	}
}

func liveDetector(t *testing.T) *EnvironmentDetector {
	t.Helper()
	// Stub the live probes (uptime, recent files, hypervisor) so classify()
	// is hermetic: it must not depend on the test host's uptime, profile
	// freshness, or virtualization.
	return &EnvironmentDetector{
		cpuCores:        8,
		totalRAM:        16 * 1024 * 1024 * 1024,
		users:           3,
		services:        80,
		stubEnv:         true,
		stubUptimeMin:   120,
		stubRecentFiles: 10,
		stubHypervisor:  false,
	}
}

func TestClassifyWorkstationSQLIsNotHighValue(t *testing.T) {
	ed := liveDetector(t)
	ed.isSQL = true
	ed.isServer = false
	ed.classify()
	if ed.profile == nil {
		t.Fatal("nil profile")
	}
	if ed.profile.Class == EnvHighValue {
		t.Fatalf("workstation with SQL tools classified as %s", ed.profile.ClassLabel)
	}
	if !ed.profile.AllowShell {
		t.Fatal("workstation profile blocked shell")
	}
}

func TestClassifyDomainJoinedWorkstationIsNotDC(t *testing.T) {
	ed := liveDetector(t)
	ed.isDomainJoined = true
	ed.domainName = "CORP"
	ed.isServer = false
	ed.classify()
	if ed.profile.Class == EnvHighValue {
		t.Fatal("domain-joined workstation must not be high_value")
	}
}

func TestCorporateScreenCapturePolicy(t *testing.T) {
	// be6958d0 decision: corporate+EDR keeps operator triage (shell/screen);
	// only high-value/server get full lockdown under EDR.
	ordinary := adjustedOpsProfile(EnvCorporate, false)
	if !ordinary.AllowScreenCapture {
		t.Fatal("ordinary corporate workstation must allow screen capture")
	}
	managed := adjustedOpsProfile(EnvCorporate, true)
	if !managed.AllowScreenCapture {
		t.Fatal("EDR-managed corporate workstation must keep screen capture")
	}
	locked := adjustedOpsProfile(EnvHighValue, true)
	if locked.AllowScreenCapture {
		t.Fatal("EDR-managed high-value server must block screen capture")
	}
}

func TestForceReanalyzeDoesNotDeadlock(t *testing.T) {
	ed := liveDetector(t)
	done := make(chan *OpsProfile, 1)
	go func() { done <- ed.ForceReanalyze() }()
	select {
	case p := <-done:
		if p == nil {
			t.Fatal("nil profile")
		}
	// ForceReanalyze -> Analyze does real WMI / process-environment probing
	// (~4s when the suite runs this package alone). Under the full `-count=1`
	// run many packages contend for CPU and the probe can take 3x that, so a
	// tight 10s deadline false-failed as "deadlocked" while the work was only
	// slow. A genuine deadlock NEVER returns, so a generous bound still catches
	// it; 60s removes the contention flake without weakening the guard.
	case <-time.After(60 * time.Second):
		t.Fatal("ForceReanalyze deadlocked")
	}
}

func TestClassifyHighValueServerKeepsShell(t *testing.T) {
	ed := liveDetector(t)
	ed.isServer = true
	ed.isDC = true
	ed.classify()
	if ed.profile.Class != EnvHighValue {
		t.Fatalf("got class %s", ed.profile.ClassLabel)
	}
	if !ed.profile.AllowShell {
		t.Fatal("high_value server must still allow shell")
	}
	if ed.profile.AllowInjection {
		t.Fatal("high_value should still suppress injection")
	}
}
