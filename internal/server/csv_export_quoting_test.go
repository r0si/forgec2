package server

import (
	"bytes"
	"encoding/csv"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

// TestTimelineExportQuotesCommas pins the CSV quoting that the hand-built rows
// lacked: a Details value containing a comma (or a newline) must stay inside its
// own column, otherwise every later column shifts and the report silently
// associates a timestamp with the wrong agent.
func TestTimelineExportQuotesCommas(t *testing.T) {
	s := mustTenantServer(t)
	// The timeline query is scoped to the caller's visible usernames, so the
	// audit row must belong to a user this operator can see.
	seedAIUser(t, s, "admin1", 1)
	seedIOCAgent(t, s, "44444444-4444-4444-4444-444444444444")

	// An audit record whose details contain a comma and a quote.
	rec := db.AuditLog{
		User:      "admin1",
		Action:    "agent_delete",
		Resource:  "agent",
		Details:   `deleted "dc01", 3 sessions dropped`,
		Success:   true,
		CreatedAt: time.Now(),
	}
	if err := s.db.Create(&rec).Error; err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/timeline/export", nil)
	c.Set("user", "admin1")
	c.Set("user_role", db.RoleAdmin)
	c.Set("user_id", uint(1))
	s.handleTimelineExport(c)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	records, err := csv.NewReader(bytes.NewReader(w.Body.Bytes())).ReadAll()
	if err != nil {
		t.Fatalf("the export is not parseable CSV: %v\n%s", err, w.Body.String())
	}
	if len(records) < 2 {
		t.Fatalf("records = %d", len(records))
	}
	header := records[0]
	if len(header) != 9 {
		t.Fatalf("header columns = %d (%v)", len(header), header)
	}
	for _, row := range records[1:] {
		if len(row) != len(header) {
			t.Fatalf("row has %d columns, header has %d: %v", len(row), len(header), row)
		}
	}
	// The details column must carry the whole value, not a fragment.
	found := false
	for _, row := range records[1:] {
		if strings.Contains(row[4], "3 sessions dropped") && strings.Contains(row[4], `"dc01"`) {
			found = true
		}
	}
	if !found {
		t.Fatalf("the comma-bearing details did not survive in one column: %v", records)
	}
}

// TestScanExportQuotesCommas covers the scan export: a service/version banner
// containing a comma must not shift the columns (which would map a banner to the
// wrong port).
func TestScanExportQuotesCommas(t *testing.T) {
	s := mustTenantServer(t)
	const agentID = "55555555-5555-5555-5555-555555555555"
	seedIOCAgent(t, s, agentID)

	task := db.Task{AgentID: agentID, Type: "portscan", Status: "completed"}
	if err := s.db.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	results := []db.ScanResult{
		{TaskID: task.ID, Port: 443, Protocol: "tcp", State: "open", Service: "https", Version: "Apache 2.4.1, OpenSSL 1.1.1", Banner: "HTTP/1.1 200 OK"},
		{TaskID: task.ID, Port: 22, Protocol: "tcp", State: "open", Service: "ssh", Version: "OpenSSH 8.9", Banner: "SSH-2.0"},
	}
	for i := range results {
		if err := s.db.Create(&results[i]).Error; err != nil {
			t.Fatal(err)
		}
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/scan/results/1/export", nil)
	c.Params = gin.Params{{Key: "taskId", Value: itoaUint(task.ID)}}
	c.Set("user", "admin1")
	c.Set("user_role", db.RoleAdmin)
	c.Set("user_id", uint(1))
	s.handleExportScanResults(c)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", w.Code, w.Body.String())
	}
	records, err := csv.NewReader(bytes.NewReader(w.Body.Bytes())).ReadAll()
	if err != nil {
		t.Fatalf("the export is not parseable CSV: %v\n%s", err, w.Body.String())
	}
	if len(records) != 3 {
		t.Fatalf("records = %d, want header + 2 rows", len(records))
	}
	for _, row := range records {
		if len(row) != 6 {
			t.Fatalf("row has %d columns: %v", len(row), row)
		}
	}
	// The comma-bearing version stayed in the Version column of the 443 row.
	var httpsRow []string
	for _, row := range records[1:] {
		if row[0] == "443" {
			httpsRow = row
		}
	}
	if httpsRow == nil {
		t.Fatalf("the 443 row is missing: %v", records)
	}
	if httpsRow[4] != "Apache 2.4.1, OpenSSL 1.1.1" {
		t.Fatalf("version column = %q", httpsRow[4])
	}
	if httpsRow[5] != "HTTP/1.1 200 OK" {
		t.Fatalf("banner column = %q", httpsRow[5])
	}
}

func itoaUint(v uint) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}
