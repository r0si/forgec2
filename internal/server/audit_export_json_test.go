package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// TestAuditExportJSONFormat covers ?format=json on the chain export: one document instead of newline-delimited
// rows, for tools that do not stream.
//
// The property that matters is the one that is NOT offered: there is no column projection here. A verifier needs
// every field the entry hash covers, so a projected export would look verifiable while being unverifiable - both
// hashes and every hashed field are present in the JSON rows, and the test asserts it rather than trusting the
// comment.
func TestAuditExportJSONFormat(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)

	now := time.Now().Truncate(time.Second)
	for i := 0; i < 3; i++ {
		if err := s.db.Create(&db.AuditLog{
			User: "alice", Action: "login", Resource: "session", TenantID: 1, Success: true,
			AgentID: "a-1", Details: "details-" + string(rune('a'+i)),
			PrevHash: "prev-hash", EntryHash: "entry-hash",
			CreatedAt: now.Add(-time.Duration(i) * time.Minute),
		}).Error; err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	call := func(query string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/audit/export"+query, nil)
		c.Set("user", "admin")
		c.Set("user_role", db.RoleAdmin)
		c.Set("user_id", uint(1))
		c.Set(middleware.TenantIDContextKey, uint(1))
		s.handleExportAuditChain(c)
		return w
	}

	// 1. The JSON variant is one document with a count, the flags and the rows.
	w := call("?format=json")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("Content-Type = %q", ct)
	}
	if cd := w.Header().Get("Content-Disposition"); !strings.HasSuffix(cd, `.json"`) {
		t.Fatalf("Content-Disposition = %q", cd)
	}
	var body struct {
		Count     int    `json:"count"`
		Truncated bool   `json:"truncated"`
		Filtered  bool   `json:"filtered"`
		Format    string `json:"format"`
		Rows      []struct {
			ID        uint   `json:"id"`
			User      string `json:"user"`
			Action    string `json:"action"`
			Resource  string `json:"resource"`
			AgentID   string `json:"agent_id"`
			Details   string `json:"details"`
			PrevHash  string `json:"prev_hash"`
			EntryHash string `json:"entry_hash"`
			CreatedAt string `json:"created_at"`
		} `json:"rows"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v (%s)", err, w.Body.String())
	}
	if body.Count != 3 || len(body.Rows) != 3 || body.Format != "json" || body.Truncated || body.Filtered {
		t.Fatalf("payload = %+v", body)
	}
	// Verification needs every hashed field and both hashes; assert it instead of trusting the comment.
	row := body.Rows[0]
	if row.PrevHash == "" || row.EntryHash == "" || row.User == "" || row.Action == "" ||
		row.Resource == "" || row.AgentID == "" || row.Details == "" || row.CreatedAt == "" {
		t.Fatalf("a JSON row is missing a field a verifier needs: %+v", row)
	}

	// 2. A filtered export says so in the document too, not only in a header.
	filtered := call("?format=json&action=login")
	var filteredBody struct {
		Filtered bool `json:"filtered"`
	}
	_ = json.Unmarshal(filtered.Body.Bytes(), &filteredBody)
	if !filteredBody.Filtered {
		t.Fatalf("a filtered JSON export did not report filtered=true: %s", filtered.Body.String())
	}

	// 3. The default is still newline-delimited JSON.
	ndjson := call("")
	if ct := ndjson.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/x-ndjson") {
		t.Fatalf("default Content-Type = %q", ct)
	}
	if lines := strings.Count(strings.TrimSpace(ndjson.Body.String()), "\n") + 1; lines != 3 {
		t.Fatalf("default export has %d line(s), want 3", lines)
	}

	// 4. The two formats are different documents, so their tags differ.
	if ndjson.Header().Get("ETag") == w.Header().Get("ETag") {
		t.Fatal("the ETag ignores the format")
	}

	// 5. An unknown format is a 400.
	if got := call("?format=xml"); got.Code != http.StatusBadRequest {
		t.Fatalf("format=xml = %d, want 400", got.Code)
	}
}
