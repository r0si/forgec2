package server

import (
	"encoding/hex"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/crypto"
	"github.com/forgec2/forgec2/internal/db"
	"github.com/google/uuid"
)

// TestEnforceKillSwitch pins the fleet stop: while armed, every beacon carries
// the token plus a per-implant MAC derived from that implant's registration key,
// so a broadcast cannot be forged for another agent; while disarmed it carries
// nothing.
func TestEnforceKillSwitch(t *testing.T) {
	s := mustTenantServer(t)
	agent := db.Implant{ID: uuid.NewString(), TenantID: 1, Hostname: "ks", Status: "online", LastSeen: time.Now()}
	if err := s.db.Create(&agent).Error; err != nil {
		t.Fatal(err)
	}

	// Disarmed: nothing attached.
	resp := beaconResponse{}
	s.enforceKillSwitch(agent, &resp)
	if resp.KillSwitch != "" || resp.KillSwitchMAC != "" {
		t.Fatalf("a disarmed kill switch attached %+v", resp)
	}

	// Armed: token + MAC.
	const token = "arm-token-xyz"
	s.killSwitchMu.Lock()
	s.killSwitchArmed = true
	s.killSwitchToken = token
	s.killSwitchMu.Unlock()
	t.Cleanup(func() {
		s.killSwitchMu.Lock()
		s.killSwitchArmed = false
		s.killSwitchToken = ""
		s.killSwitchMu.Unlock()
	})

	resp = beaconResponse{}
	s.enforceKillSwitch(agent, &resp)
	if resp.KillSwitch != token {
		t.Fatalf("kill switch token = %q", resp.KillSwitch)
	}
	if resp.KillSwitchMAC == "" {
		t.Fatal("no MAC was attached")
	}

	// The MAC is the one this implant's registration key derives.
	ksKey := crypto.DeriveKillSwitchKey(s.deriveRegKey(agent.ID))
	if want := hex.EncodeToString(crypto.KillSwitchHMAC(ksKey, []byte(token))); resp.KillSwitchMAC != want {
		t.Fatalf("MAC = %s, want %s", resp.KillSwitchMAC, want)
	}

	// And it does not validate for a different implant: the tag is per-agent.
	other := db.Implant{ID: uuid.NewString(), TenantID: 1, Hostname: "ks2", Status: "online", LastSeen: time.Now()}
	if err := s.db.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	otherKey := crypto.DeriveKillSwitchKey(s.deriveRegKey(other.ID))
	if hex.EncodeToString(crypto.KillSwitchHMAC(otherKey, []byte(token))) == resp.KillSwitchMAC {
		t.Fatal("two implants share a kill-switch MAC")
	}

	// Armed with an empty token is treated as disarmed (nothing to authenticate).
	s.killSwitchMu.Lock()
	s.killSwitchToken = ""
	s.killSwitchMu.Unlock()
	resp = beaconResponse{}
	s.enforceKillSwitch(agent, &resp)
	if resp.KillSwitch != "" {
		t.Fatal("an armed switch without a token attached a payload")
	}
}
