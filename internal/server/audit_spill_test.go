package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

// TestAuditQueueOverflowParksTheBatch pins the backpressure rule: a full queue
// must not lose the rows. The batch is parked on disk (the request path never
// waits on the database) and the worker writes it afterwards, with the chain
// intact.
func TestAuditQueueOverflowParksTheBatch(t *testing.T) {
	s := mustTenantServer(t)
	dataDir := t.TempDir()
	s.cfg.Server.DataDir = dataDir
	// A queue with one slot, already taken: the next batch cannot be queued.
	s.auditQueue = make(chan []db.AuditLog, 1)
	s.auditQueue <- []db.AuditLog{{Action: "filler"}}

	s.flushAuditEntries([]db.AuditLog{{
		User: "op", Action: "overflow_test", Resource: "agent", Success: true, Details: "written while the queue was full",
	}})

	if got := s.auditDropped.Load(); got != 0 {
		t.Fatalf("dropped = %d, want 0: a full queue must not lose rows", got)
	}
	if got := s.auditSpilled.Load(); got != 1 {
		t.Fatalf("spilled = %d, want 1 (the batch was parked, not lost)", got)
	}
	if pending := s.auditSpillPending(); pending != 1 {
		t.Fatalf("pending spill files = %d, want 1", pending)
	}
	var rows []db.AuditLog
	if err := s.db.Where("action = ?", "overflow_test").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("rows written = %d, want 0 (the batch is parked until the worker writes it)", len(rows))
	}

	if recovered := s.drainAuditSpill(); recovered != 1 {
		t.Fatalf("recovered = %d, want 1", recovered)
	}
	rows = nil
	if err := s.db.Where("action = ?", "overflow_test").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows after the drain = %d, want 1", len(rows))
	}
	if verdict := s.verifyAuditChain(100); !verdict.OK {
		t.Fatalf("audit chain after recovery: %s (row %d)", verdict.Reason, verdict.FirstBadID)
	}
}

// TestAuditSpillRecoversRowsAfterWriteFailure covers the other half: a batch
// that cannot be written (database unreachable) is parked on disk and written
// later, with the hash chain intact.
func TestAuditSpillRecoversRowsAfterWriteFailure(t *testing.T) {
	s := mustTenantServer(t)
	dataDir := t.TempDir()
	s.cfg.Server.DataDir = dataDir

	batch := []db.AuditLog{{
		User: "op", Action: "spill_test", Resource: "agent", Success: true, Details: "the database was down",
	}}
	if !s.spillAuditBatch(batch) {
		t.Fatal("the batch was not parked on disk")
	}
	second := []db.AuditLog{{
		User: "op", Action: "spill_test_2", Resource: "agent", Success: true, Details: "second batch",
	}}
	if !s.spillAuditBatch(second) {
		t.Fatal("the second batch was not parked on disk")
	}
	if got := s.auditSpilled.Load(); got != 2 {
		t.Fatalf("spilled = %d, want 2", got)
	}
	if pending := s.auditSpillPending(); pending != 2 {
		t.Fatalf("pending spill files = %d, want 2", pending)
	}
	entries, err := os.ReadDir(filepath.Join(dataDir, "audit-spill"))
	if err != nil {
		t.Fatalf("read spill dir: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("spill dir = %v, want two .json batches", entries)
	}

	if recovered := s.drainAuditSpill(); recovered != 2 {
		t.Fatalf("recovered = %d, want 2", recovered)
	}
	if pending := s.auditSpillPending(); pending != 0 {
		t.Fatalf("pending spill files = %d after the drain, want 0", pending)
	}
	var rows []db.AuditLog
	if err := s.db.Where("action LIKE ?", "spill_test%").Order("id ASC").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows written = %d, want 2", len(rows))
	}
	for i, row := range rows {
		if row.EntryHash == "" {
			t.Fatalf("row %d has no entry hash: %+v", i, row)
		}
		if i > 0 && row.PrevHash != rows[i-1].EntryHash {
			t.Fatalf("row %d is not chained onto the previous row", i)
		}
	}
	if verdict := s.verifyAuditChain(100); !verdict.OK {
		t.Fatalf("audit chain after recovery: %s (row %d)", verdict.Reason, verdict.FirstBadID)
	}
}

// TestAuditSpillSurvivesAProcessRestart proves the durability claim: the parked
// batch is still on disk for the next process, and a fresh server drains it.
func TestAuditSpillSurvivesAProcessRestart(t *testing.T) {
	first := mustTenantServer(t)
	dataDir := t.TempDir()
	first.cfg.Server.DataDir = dataDir
	if !first.spillAuditBatch([]db.AuditLog{{User: "op", Action: "restart_spill_test", Success: true}}) {
		t.Fatal("the batch was not parked on disk")
	}

	// A new process (new Server) over the same data directory.
	second := mustTenantServer(t)
	second.cfg.Server.DataDir = dataDir
	if pending := second.auditSpillPending(); pending != 1 {
		t.Fatalf("pending spill files after restart = %d, want 1", pending)
	}
	if recovered := second.drainAuditSpill(); recovered != 1 {
		t.Fatalf("recovered after restart = %d, want 1", recovered)
	}
	var rows []db.AuditLog
	if err := second.db.Where("action = ?", "restart_spill_test").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows written after restart = %d, want 1", len(rows))
	}
}

// TestReadyReportsParkedAuditBatches pins the operator-visible half: batches
// waiting on disk are a backlog the probe must name.
func TestReadyReportsParkedAuditBatches(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)
	s.cfg.Server.DataDir = t.TempDir()
	if !s.spillAuditBatch([]db.AuditLog{{User: "op", Action: "parked_test", Success: true}}) {
		t.Fatal("the batch was not parked on disk")
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/ready", nil)
	c.Set("user", "op")
	c.Set("user_role", db.RoleAdmin)
	c.Set("user_id", uint(1))
	s.handleReadyCheck(c)

	if w.Code != http.StatusOK {
		t.Fatalf("ready status = %d, want 200", w.Code)
	}
	var body struct {
		Checks   map[string]interface{} `json:"checks"`
		Warnings []string               `json:"warnings"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if got, _ := body.Checks["audit_spill_pending"].(float64); got != 1 {
		t.Fatalf("audit_spill_pending = %v, want 1", body.Checks["audit_spill_pending"])
	}
	found := false
	for _, warning := range body.Warnings {
		if strings.Contains(warning, "parked") {
			found = true
		}
	}
	if !found {
		t.Fatalf("warnings do not name the parked batches: %v", body.Warnings)
	}
}

// TestAuditSpillIgnoresUnrelatedFiles keeps the drain from touching anything
// that is not a parked batch.
func TestAuditSpillIgnoresUnrelatedFiles(t *testing.T) {
	s := mustTenantServer(t)
	dataDir := t.TempDir()
	s.cfg.Server.DataDir = dataDir
	spillDir := filepath.Join(dataDir, "audit-spill")
	if err := os.MkdirAll(spillDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(spillDir, "notes.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	if recovered := s.drainAuditSpill(); recovered != 0 {
		t.Fatalf("recovered = %d, want 0", recovered)
	}
	if _, err := os.Stat(filepath.Join(spillDir, "notes.txt")); err != nil {
		t.Fatalf("the drain touched an unrelated file: %v", err)
	}
}
