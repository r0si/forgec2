package server

import (
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/db"
)

// TestCheckAgentAlertsBuckets pins the online/stale/offline transition, the
// status-event log and the offline alert rule. It is the regression guard for
// the stale-threshold unit bug: with a nanosecond stale band, an agent quiet for
// two minutes flipped straight to offline and the stale transition never
// happened.
func TestCheckAgentAlertsBuckets(t *testing.T) {
	_, m := monitorTestServer(t)
	m.server.cfg.Server.OfflineThreshold = 60 // stale = 180s
	now := time.Now()

	agents := []db.Implant{
		{ID: "fresh", TenantID: 1, Hostname: "fresh", Status: "online", LastSeen: now.Add(-5 * time.Second)},
		{ID: "quiet", TenantID: 1, Hostname: "quiet", Status: "online", LastSeen: now.Add(-2 * time.Minute)},
		{ID: "gone", TenantID: 1, Hostname: "gone", Status: "online", LastSeen: now.Add(-30 * time.Minute)},
	}
	for i := range agents {
		if err := m.server.db.Create(&agents[i]).Error; err != nil {
			t.Fatal(err)
		}
	}

	// An offline rule with a small threshold fires for the agents past it.
	rule := db.AlertRule{Name: "offline", Type: "agent_offline", Threshold: 60, Enabled: true}
	if err := m.server.db.Create(&rule).Error; err != nil {
		t.Fatal(err)
	}
	if err := m.server.db.Model(&db.AlertRule{}).Where("id = ?", rule.ID).Update("enabled", true).Error; err != nil {
		t.Fatal(err)
	}

	m.checkAgentAlerts()

	statusOf := func(id string) string {
		var a db.Implant
		if err := m.server.db.Where("id = ?", id).First(&a).Error; err != nil {
			t.Fatalf("reload %s: %v", id, err)
		}
		return a.Status
	}

	if got := statusOf("fresh"); got != "online" {
		t.Fatalf("fresh agent = %q, want online", got)
	}
	if got := statusOf("quiet"); got != "stale" {
		t.Fatalf("agent quiet for 2 minutes = %q, want stale (the stale band must exist)", got)
	}
	if got := statusOf("gone"); got != "offline" {
		t.Fatalf("agent quiet for 30 minutes = %q, want offline", got)
	}

	// One status event per transition, and none for the fresh agent.
	var events []db.AgentStatusEvent
	if err := m.server.db.Where("agent_id IN ?", []string{"fresh", "quiet", "gone"}).Find(&events).Error; err != nil {
		t.Fatal(err)
	}
	byAgent := map[string]string{}
	for _, ev := range events {
		byAgent[ev.AgentID] = ev.Status
	}
	if _, ok := byAgent["fresh"]; ok {
		t.Fatalf("fresh agent produced a status event: %v", byAgent)
	}
	if byAgent["quiet"] != "stale" || byAgent["gone"] != "offline" {
		t.Fatalf("status events = %v", byAgent)
	}

	// The offline rule has its own threshold (60s here), independent of the
	// status bucket: every agent silent longer than that alerts, which is why
	// the 2-minute agent alerts even though its status is only "stale".
	var alerts []db.Alert
	if err := m.server.db.Where("type = ?", "agent_offline").Find(&alerts).Error; err != nil {
		t.Fatal(err)
	}
	alerted := map[string]bool{}
	for _, a := range alerts {
		alerted[a.Source] = true
	}
	if alerted["fresh"] {
		t.Fatalf("alert fired for an agent inside the rule threshold: %v", alerted)
	}
	if !alerted["quiet"] || !alerted["gone"] {
		t.Fatalf("silent agents did not alert: %v", alerted)
	}

	// A second tick must not duplicate the active alert.
	m.checkAgentAlerts()
	var after int64
	m.server.db.Model(&db.Alert{}).Where("type = ?", "agent_offline").Count(&after)
	if after != int64(len(alerts)) {
		t.Fatalf("second tick created duplicates: %d -> %d", len(alerts), after)
	}
}
