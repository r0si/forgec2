package server

import (
	"math"
	"net/http"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

func monitorTestServer(t *testing.T) (*Server, *MonitorCollector) {
	t.Helper()
	s := mustTenantServer(t)
	m := NewMonitorCollector(s)
	s.monitorCollector = m
	return s, m
}

// TestHandleGetSystemMetrics covers the read endpoint and its NaN contract:
// a failed OS probe is reported as missing data, not a fabricated 0%.
func TestHandleGetSystemMetrics(t *testing.T) {
	s := mustTenantServer(t)
	// No collector yet: 503 rather than an empty success.
	c, w := missingAPICtx(http.MethodGet, "/api/monitor/metrics", "", nil)
	s.handleGetSystemMetrics(c)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("nil collector = %d", w.Code)
	}

	m := NewMonitorCollector(s)
	s.monitorCollector = m
	m.mu.Lock()
	m.lastMetrics = db.SystemMetric{
		Hostname:    "mon-host",
		CPULoad:     42.5,
		MemoryUsed:  7,
		MemoryTotal: 8,
		DiskUsed:    90,
		DiskTotal:   100,
		CreatedAt:   time.Now(),
	}
	m.mu.Unlock()

	c, w = missingAPICtx(http.MethodGet, "/api/monitor/metrics", "", nil)
	s.handleGetSystemMetrics(c)
	if w.Code != http.StatusOK {
		t.Fatalf("metrics = %d %s", w.Code, w.Body.String())
	}
	var out struct {
		Memory struct {
			Percent float64 `json:"percent"`
		} `json:"memory"`
		Hostname string  `json:"hostname"`
		CPU      float64 `json:"cpu"`
		Disk     struct {
			Percent float64 `json:"percent"`
		} `json:"disk"`
	}
	unmarshalData(t, w.Body.Bytes(), &out)
	if out.Hostname != "mon-host" || math.Abs(out.CPU-42.5) > 0.001 {
		t.Fatalf("metrics payload = %+v", out)
	}
	if math.Abs(out.Memory.Percent-87.5) > 0.001 || math.Abs(out.Disk.Percent-90) > 0.001 {
		t.Fatalf("percentages = mem %v disk %v", out.Memory.Percent, out.Disk.Percent)
	}

	// NaN readings are omitted entirely.
	m.mu.Lock()
	m.lastMetrics = db.SystemMetric{Hostname: "nan-host", CPULoad: math.NaN(), DiskUsed: math.NaN(), DiskTotal: math.NaN(), MemoryTotal: 0}
	m.mu.Unlock()
	c, w = missingAPICtx(http.MethodGet, "/api/monitor/metrics", "", nil)
	s.handleGetSystemMetrics(c)
	if w.Code != http.StatusOK {
		t.Fatalf("nan metrics = %d %s", w.Code, w.Body.String())
	}
	var raw map[string]any
	unmarshalData(t, w.Body.Bytes(), &raw)
	if _, ok := raw["cpu"]; ok {
		t.Fatal("NaN cpu was reported as a value")
	}
	if _, ok := raw["disk"]; ok {
		t.Fatal("NaN disk was reported as a value")
	}
	if _, ok := raw["memory"]; !ok {
		t.Fatal("memory should still be reported (zero total is a valid reading)")
	}
	if m.GetLatestMetrics().Hostname != "nan-host" {
		t.Fatal("GetLatestMetrics did not return the stored snapshot")
	}
}

// TestCheckSystemAlerts covers the threshold evaluation and the dedupe: the
// same rule must not open a second active alert on the next tick.
func TestCheckSystemAlerts(t *testing.T) {
	_, m := monitorTestServer(t)

	rules := []db.AlertRule{
		{Name: "cpu", Type: "cpu_high", Threshold: 50, Enabled: true},
		{Name: "cpu-quiet", Type: "cpu_high", Threshold: 99, Enabled: true},
		{Name: "mem", Type: "memory_high", Threshold: 80, Enabled: true},
		{Name: "disk", Type: "disk_high", Threshold: 80, Enabled: true},
		{Name: "disabled", Type: "cpu_high", Threshold: 1, Enabled: false},
	}
	for i := range rules {
		if err := m.server.db.Create(&rules[i]).Error; err != nil {
			t.Fatalf("seed rule: %v", err)
		}
	}
	// GORM applies `default:true` to a zero-value bool, so a rule seeded with
	// Enabled:false is written enabled. The production create handler works
	// around this with an explicit update; tests must do the same.
	if err := m.server.db.Model(&db.AlertRule{}).Where("id = ?", rules[4].ID).Update("enabled", false).Error; err != nil {
		t.Fatal(err)
	}

	m.mu.Lock()
	m.lastMetrics = db.SystemMetric{
		Hostname:    "alert-host",
		CPULoad:     90,
		MemoryUsed:  9,
		MemoryTotal: 10,
		DiskUsed:    95,
		DiskTotal:   100,
	}
	m.mu.Unlock()

	m.checkSystemAlerts()

	var alerts []db.Alert
	if err := m.server.db.Where("source = ?", "system").Find(&alerts).Error; err != nil {
		t.Fatal(err)
	}
	if len(alerts) != 3 {
		t.Fatalf("alerts = %d, want 3 (cpu, mem, disk; the 99%% rule and the disabled one must not fire)", len(alerts))
	}
	byRule := map[uint]db.Alert{}
	for _, a := range alerts {
		byRule[a.RuleID] = a
	}
	if byRule[rules[0].ID].SourceName != "alert-host" {
		t.Fatalf("alert source name = %q", byRule[rules[0].ID].SourceName)
	}
	if byRule[rules[0].ID].Status != "active" {
		t.Fatalf("alert status = %q", byRule[rules[0].ID].Status)
	}

	// A second tick with the same readings must not duplicate the active rows.
	m.checkSystemAlerts()
	var after int64
	m.server.db.Model(&db.Alert{}).Where("source = ?", "system").Count(&after)
	if after != 3 {
		t.Fatalf("second tick produced %d alerts, want still 3 (dedupe)", after)
	}

	// A NaN CPU probe must not evaluate the cpu rule at all.
	if err := m.server.db.Where("source = ?", "system").Delete(&db.Alert{}).Error; err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.lastMetrics.CPULoad = math.NaN()
	m.mu.Unlock()
	m.checkSystemAlerts()
	var cpuAlerts int64
	m.server.db.Model(&db.Alert{}).Where("source = ? AND rule_id = ?", "system", rules[0].ID).Count(&cpuAlerts)
	if cpuAlerts != 0 {
		t.Fatalf("NaN cpu produced %d alerts", cpuAlerts)
	}
}

var _ = gin.Params{}
