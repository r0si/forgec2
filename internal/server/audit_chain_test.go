package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/config"
	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

func newAuditTestServer(t *testing.T) *Server {
	t.Helper()
	gin.SetMode(gin.TestMode)
	return &Server{db: newContractDB(t), cfg: config.DefaultConfig()}
}

// Regression: two concurrent flushes must not commit entries sharing the same
// prev_hash (the read-last-entry + insert sequence used to race).
func TestFlushAuditEntries_ConcurrentChainIntegrity(t *testing.T) {
	s := newAuditTestServer(t)

	const writers = 8
	const perWriter = 10
	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			entries := make([]db.AuditLog, 0, perWriter)
			for i := 0; i < perWriter; i++ {
				entries = append(entries, db.AuditLog{
					User:    fmt.Sprintf("user-%d", w),
					Action:  "concurrent_test",
					Success: true,
					Details: fmt.Sprintf("writer=%d item=%d", w, i),
				})
			}
			s.flushAuditEntries(entries)
		}(w)
	}
	wg.Wait()

	var rows []db.AuditLog
	if err := s.db.Order("id ASC").Find(&rows).Error; err != nil {
		t.Fatalf("load audit rows: %v", err)
	}
	if len(rows) != writers*perWriter {
		t.Fatalf("expected %d rows, got %d", writers*perWriter, len(rows))
	}
	// Every row except the first must reference the immediately preceding row's
	// entry hash, and prev_hash must be unique per row (a broken chain would
	// duplicate prev_hash values or leave empty hashes).
	prevHashes := make(map[string]bool)
	lastHash := ""
	for i, row := range rows {
		if i == 0 {
			if row.PrevHash != "" {
				t.Fatalf("first row prev_hash = %q, want empty", row.PrevHash)
			}
		} else if row.PrevHash != lastHash {
			t.Fatalf("row %d prev_hash mismatch: got %q want %q", i, row.PrevHash, lastHash)
		}
		if row.EntryHash == "" {
			t.Fatalf("row %d has empty entry_hash", i)
		}
		if prevHashes[row.PrevHash] {
			t.Fatalf("duplicate prev_hash %q at row %d — chain race", row.PrevHash, i)
		}
		prevHashes[row.PrevHash] = true
		lastHash = row.EntryHash
	}
}

// Regression: operator actions must be part of the hash chain (they previously
// bypassed it via a raw Create with empty prev_hash/entry_hash).
func TestLogOperatorAction_EntersHashChain(t *testing.T) {
	s := newAuditTestServer(t)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest(http.MethodPost, "/settings/jwt/regenerate", nil)
	c.Set("user", "admin-user")
	c.Set("user_role", "admin")

	s.LogOperatorAction(c, OperatorAction{
		Action:    "test_action",
		Resource:  "test",
		TargetID:  "t-1",
		Details:   "unit test",
		RiskLevel: "low",
	})

	var rows []db.AuditLog
	if err := s.db.Order("id ASC").Find(&rows).Error; err != nil {
		t.Fatalf("load audit rows: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0].EntryHash == "" || rows[0].PrevHash != "" {
		t.Fatalf("expected chained entry (empty prev, non-empty entry hash), got prev=%q entry=%q",
			rows[0].PrevHash, rows[0].EntryHash)
	}

	// A second action must chain onto the first.
	s.LogOperatorAction(c, OperatorAction{
		Action:    "test_action_2",
		Resource:  "test",
		RiskLevel: "low",
	})
	var after []db.AuditLog
	if err := s.db.Order("id ASC").Find(&after).Error; err != nil {
		t.Fatalf("load audit rows: %v", err)
	}
	if len(after) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(after))
	}
	if after[1].PrevHash != after[0].EntryHash {
		t.Fatalf("second entry prev_hash = %q, want first entry hash %q", after[1].PrevHash, after[0].EntryHash)
	}
}

// Regression: the async worker must persist every enqueued entry with an
// intact hash chain, and drain on context cancel.
func TestAuditWorker_PersistsAndDrains(t *testing.T) {
	s := newAuditTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	s.ctx = ctx
	s.metrics = NewMetricsCollector(s)
	s.startAuditWorker()

	const writers = 4
	const perWriter = 25
	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			entries := make([]db.AuditLog, 0, perWriter)
			for i := 0; i < perWriter; i++ {
				entries = append(entries, db.AuditLog{
					User:    fmt.Sprintf("async-%d", w),
					Action:  "async_test",
					Success: true,
					Details: fmt.Sprintf("writer=%d item=%d", w, i),
				})
			}
			s.flushAuditEntries(entries)
		}(w)
	}
	wg.Wait()
	cancel()
	s.wg.Wait()

	var rows []db.AuditLog
	if err := s.db.Order("id ASC").Find(&rows).Error; err != nil {
		t.Fatalf("load audit rows: %v", err)
	}
	if len(rows) != writers*perWriter {
		t.Fatalf("expected %d rows, got %d (dropped=%d)", writers*perWriter, len(rows), s.auditDropped.Load())
	}
	lastHash := ""
	for i, row := range rows {
		if i > 0 && row.PrevHash != lastHash {
			t.Fatalf("row %d prev_hash mismatch", i)
		}
		if row.EntryHash == "" {
			t.Fatalf("row %d has empty entry_hash", i)
		}
		lastHash = row.EntryHash
	}
	if got := s.auditDropped.Load(); got != 0 {
		t.Fatalf("dropped = %d, want 0", got)
	}
}

// Regression: lateral-movement credentials in audit details must be masked
// (pass/ticket/bare key), while ordinary text passes through.
func TestSanitizeDetailsMasksCreds(t *testing.T) {
	cases := []struct{ in, want string }{
		{`lateral smb user=admin pass=Secret123`, `lateral smb user=admin pass:*****`},
		{`"ticket":"c81e728d9d4c2f636f067f5e modules"`, `ticket:*****`},
		{`key=AKIAIOSFODNN7EXAMPLE data`, `key:***** data`},
		{`password=Secret123`, `password:*****`},
		{`clean operational note`, `clean operational note`},
	}
	for _, tc := range cases {
		if got := sanitizeDetails(tc.in); got != tc.want {
			t.Errorf("sanitizeDetails(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestHandleBuildList_SnapshotsFields ensures the build list reads job fields
// without racing the build goroutine (values must round-trip through JSON).
func TestHandleBuildList_SnapshotsFields(t *testing.T) {
	s := newAuditTestServer(t)
	s.buildJobsMu = sync.RWMutex{}
	s.buildJobs = make(map[string]*BuildJob)
	s.buildJobs["job-1"] = &BuildJob{
		ID:          "job-1",
		Status:      "completed",
		Output:      "/tmp/out.exe",
		Platform:    "windows",
		Format:      "exe",
		Filename:    "agent.exe",
		CreatedAt:   time.Now(),
		CompletedAt: time.Now(),
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest(http.MethodGet, "/generate/builds", nil)

	s.handleBuildList(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Builds []struct {
			ID          string `json:"id"`
			Status      string `json:"status"`
			Filename    string `json:"filename"`
			Error       string `json:"error,omitempty"`
			CompletedAt string `json:"completed_at"`
		} `json:"builds"`
	}
	unmarshalData(t, w.Body.Bytes(), &resp)
	if len(resp.Builds) != 1 {
		t.Fatalf("expected 1 build, got %d; body=%s", len(resp.Builds), w.Body.String())
	}
	if resp.Builds[0].Status != "completed" || resp.Builds[0].Filename != "agent.exe" {
		t.Fatalf("unexpected build fields: %+v", resp.Builds[0])
	}
}
