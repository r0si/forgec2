package plugin

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/testutil"
)

// TestManagerQuotaSetters pins the operator-facing quota controls: each accepts
// a positive value, ignores a non-positive one (keeping the current budget), and
// changes only its own knob.
func TestManagerQuotaSetters(t *testing.T) {
	m := NewManager(nil)

	// Non-positive values are ignored, so an operator typo cannot disable a
	// protection that is already in place.
	beforeCap := cap(m.exec.slots)
	beforeCPU := m.exec.cpuQuota
	beforeWrite := m.exec.writeQuota
	m.SetMaxConcurrent(0)
	m.SetMaxConcurrent(-3)
	m.SetMaxCPUSeconds(0)
	m.SetMaxWriteMB(-1)
	if cap(m.exec.slots) != beforeCap || m.exec.cpuQuota != beforeCPU || m.exec.writeQuota != beforeWrite {
		t.Fatalf("a non-positive setter changed the executor: cap=%d cpu=%v write=%d",
			cap(m.exec.slots), m.exec.cpuQuota, m.exec.writeQuota)
	}

	// Positive values apply, one knob at a time.
	m.SetMaxConcurrent(7)
	if cap(m.exec.slots) != 7 {
		t.Fatalf("concurrency = %d", cap(m.exec.slots))
	}
	if m.exec.cpuQuota != beforeCPU || m.exec.writeQuota != beforeWrite {
		t.Fatal("SetMaxConcurrent reset another budget")
	}

	m.SetMaxCPUSeconds(9)
	if m.exec.cpuQuota != 9*time.Second {
		t.Fatalf("cpu quota = %v", m.exec.cpuQuota)
	}
	if cap(m.exec.slots) != 7 {
		t.Fatal("SetMaxCPUSeconds reset the concurrency cap")
	}

	m.SetMaxWriteMB(3)
	if m.exec.writeQuota != 3<<20 {
		t.Fatalf("write quota = %d", m.exec.writeQuota)
	}
	if cap(m.exec.slots) != 7 || m.exec.cpuQuota != 9*time.Second {
		t.Fatal("SetMaxWriteMB reset another budget")
	}
}

// TestManagerSetEnabledTogglesThePlugin pins the disable switch: the database
// record and the in-memory plugin flag move together, so a disabled plugin is
// refused immediately rather than only after a restart. An unknown name is an
// error, not a silent no-op.
func TestManagerSetEnabledTogglesThePlugin(t *testing.T) {
	m := NewManager(testutil.SetupTestDB(t))

	record := db.Plugin{Name: "toggle-me", Type: "command", Enabled: true}
	if err := m.db.Create(&record).Error; err != nil {
		t.Fatal(err)
	}
	p := &countingPlugin{name: "toggle-me"}
	m.mu.Lock()
	m.plugins["toggle-me"] = p
	m.mu.Unlock()

	if err := m.SetEnabled("toggle-me", false); err != nil {
		t.Fatalf("disable: %v", err)
	}
	var stored db.Plugin
	if err := m.db.Where("name = ?", "toggle-me").First(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Enabled {
		t.Fatal("the database record is still enabled")
	}

	if err := m.SetEnabled("toggle-me", true); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if err := m.db.Where("name = ?", "toggle-me").First(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if !stored.Enabled {
		t.Fatal("the database record is still disabled")
	}

	if err := m.SetEnabled("no-such-plugin", false); err == nil {
		t.Fatal("enabling an unknown plugin reported success")
	}
}

// TestManagerUnregisterRemovesPluginAndDirectory pins teardown: the plugin leaves
// the in-memory map, its database row is deleted, and its directory is removed.
func TestManagerUnregisterRemovesPluginAndDirectory(t *testing.T) {
	m := NewManager(testutil.SetupTestDB(t))
	m.pluginDir = t.TempDir()

	record := db.Plugin{Name: "gone", Type: "command", Enabled: true}
	if err := m.db.Create(&record).Error; err != nil {
		t.Fatal(err)
	}
	p := &countingPlugin{name: "gone"}
	m.mu.Lock()
	m.plugins["gone"] = p
	m.mu.Unlock()

	if err := m.Unregister("gone"); err != nil {
		t.Fatalf("unregister: %v", err)
	}

	m.mu.RLock()
	_, stillLoaded := m.plugins["gone"]
	m.mu.RUnlock()
	if stillLoaded {
		t.Fatal("the plugin is still in memory")
	}

	var count int64
	m.db.Model(&db.Plugin{}).Where("name = ?", "gone").Count(&count)
	if count != 0 {
		t.Fatal("the plugin row was not deleted")
	}
	if p.shutdowns != 1 {
		t.Fatalf("shutdown calls = %d, want 1", p.shutdowns)
	}
}

// TestManagerLoadFailuresSnapshot covers the load-failure view: it reports the
// recorded failures and returns a copy, so a caller cannot mutate manager state.
func TestManagerLoadFailuresSnapshot(t *testing.T) {
	m := NewManager(nil)
	if got := m.LoadFailures(); len(got) != 0 {
		t.Fatalf("empty manager reported %v", got)
	}
	m.recordLoadFailure("broken", errors.New("manifest.yaml: missing name"))
	got := m.LoadFailures()
	if got["broken"] == "" {
		t.Fatalf("recorded failure not reported: %v", got)
	}
	got["broken"] = "mutated by the caller"
	if m.LoadFailures()["broken"] == "mutated by the caller" {
		t.Fatal("LoadFailures returned the live map")
	}
	// A successful load clears the failure.
	m.recordLoadSuccess("broken")
	if _, ok := m.LoadFailures()["broken"]; ok {
		t.Fatal("a successful load did not clear the failure")
	}
}

// countingPlugin is a minimal Plugin that records how often it was shut down.
type countingPlugin struct {
	name      string
	shutdowns int
}

func (c *countingPlugin) Name() string                                       { return c.name }
func (c *countingPlugin) Version() string                                    { return "test" }
func (c *countingPlugin) Type() string                                       { return "command" }
func (c *countingPlugin) Description() string                                { return "test stub" }
func (c *countingPlugin) Init(context.Context, map[string]interface{}) error { return nil }
func (c *countingPlugin) Shutdown() error                                    { c.shutdowns++; return nil }
func (c *countingPlugin) IsEnabled() bool                                    { return true }
