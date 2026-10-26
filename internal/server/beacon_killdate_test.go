package server

import (
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/db"
)

// TestEnforceKillDate pins the kill-date behaviour: past the date the server
// injects a kill task (so the agent self-destructs on its next check-in), it
// does so only once while that task is pending, and an agent without a kill
// date (or before it) is left alone. Getting this wrong in either direction is
// bad: a missed kill leaves an implant running past the engagement, and a
// spurious one destroys an agent mid-operation.
func TestEnforceKillDate(t *testing.T) {
	s := mustTenantServer(t)
	seedAIUser(t, s, "admin1", 1)
	now := time.Now()

	past := now.Add(-time.Hour)
	future := now.Add(time.Hour)

	newAgent := func(id string, killDate *time.Time) db.Implant {
		a := db.Implant{ID: id, TenantID: 1, Hostname: id, Status: "online", KillDate: killDate, LastSeen: now}
		if err := s.db.Create(&a).Error; err != nil {
			t.Fatalf("seed agent %s: %v", id, err)
		}
		return a
	}

	// No kill date: nothing injected.
	noDate := newAgent("kd-none", nil)
	resp := &beaconResponse{}
	s.enforceKillDate(noDate, resp, now)
	if len(resp.Tasks) != 0 {
		t.Fatalf("an agent without a kill date got %d task(s)", len(resp.Tasks))
	}

	// Kill date in the future: nothing injected.
	notYet := newAgent("kd-future", &future)
	resp = &beaconResponse{}
	s.enforceKillDate(notYet, resp, now)
	if len(resp.Tasks) != 0 {
		t.Fatalf("an agent before its kill date got %d task(s)", len(resp.Tasks))
	}

	// Kill date passed: one kill task is injected and persisted.
	expired := newAgent("kd-past", &past)
	resp = &beaconResponse{}
	s.enforceKillDate(expired, resp, now)
	if len(resp.Tasks) != 1 {
		t.Fatalf("expired agent got %d task(s), want 1", len(resp.Tasks))
	}
	if resp.Tasks[0].Type != "kill" {
		t.Fatalf("injected task type = %q, want kill", resp.Tasks[0].Type)
	}
	var killRows []db.Task
	if err := s.db.Where("agent_id = ? AND type = ?", "kd-past", "kill").Find(&killRows).Error; err != nil {
		t.Fatal(err)
	}
	if len(killRows) != 1 {
		t.Fatalf("persisted kill tasks = %d, want 1", len(killRows))
	}
	if killRows[0].Status != "running" {
		t.Fatalf("kill task status = %q, want running so the agent picks it up", killRows[0].Status)
	}

	// A second response that already carries the kill task does not duplicate
	// it (the same beacon can be re-rendered before the agent checks in).
	resp2 := &beaconResponse{Tasks: []task{{ID: killRows[0].ID, Type: "kill"}}}
	s.enforceKillDate(expired, resp2, now)
	if len(resp2.Tasks) != 1 {
		t.Fatalf("kill task duplicated: %d tasks", len(resp2.Tasks))
	}
	var after int64
	s.db.Model(&db.Task{}).Where("agent_id = ? AND type = ?", "kd-past", "kill").Count(&after)
	if after != 1 {
		t.Fatalf("persisted kill tasks after a second render = %d", after)
	}
}
