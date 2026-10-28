package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// TestExportCapabilities covers the machine-readable description of the download surface.
//
// The values are derived from the code that enforces them, and this test is what makes that claim true: a
// capabilities document that drifts from the behaviour is worse than none, because a client trusts it.
func TestExportCapabilities(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/exports/capabilities", nil)
	c.Set("user", "op")
	c.Set("user_role", db.RoleAdmin)
	c.Set("user_id", uint(1))
	c.Set(middleware.TenantIDContextKey, uint(1))
	s.handleExportCapabilities(c)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", w.Code, w.Body.String())
	}
	var resp struct {
		Data struct {
			TaskExport   exportCapabilities         `json:"task_export"`
			AuditExport  exportCapabilities         `json:"audit_export"`
			Output       exportCapabilities         `json:"task_output_download"`
			PluginsIndex exportCapabilities         `json:"plugins_index"`
			Limits       map[string]json.RawMessage `json:"limits"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v (%s)", err, w.Body.String())
	}

	// 1. The task export's field list IS the catalogue, not a copy maintained by hand.
	if len(resp.Data.TaskExport.Fields) != len(exportFields) {
		t.Fatalf("fields = %d, the catalogue has %d", len(resp.Data.TaskExport.Fields), len(exportFields))
	}
	for i, field := range exportFields {
		if resp.Data.TaskExport.Fields[i] != field.name {
			t.Fatalf("field %d = %q, want %q (order matters: it is the default column order)", i,
				resp.Data.TaskExport.Fields[i], field.name)
		}
	}

	// 2. The row cap is the constant the handler enforces.
	if resp.Data.TaskExport.MaxRows != ExportTaskLimit {
		t.Fatalf("max_rows = %d, want %d", resp.Data.TaskExport.MaxRows, ExportTaskLimit)
	}

	// 3. The minimal preset is the same slice the parser uses.
	minimal := resp.Data.TaskExport.Presets["minimal"]
	if len(minimal) != len(exportMinimalFields) {
		t.Fatalf("minimal preset = %v, want %v", minimal, exportMinimalFields)
	}
	for i := range minimal {
		if minimal[i] != exportMinimalFields[i] {
			t.Fatalf("minimal preset = %v, want %v", minimal, exportMinimalFields)
		}
	}

	// 4. The audit byte cap comes from the variable, so a later tuning of it cannot leave this stale.
	var maxBytes int64
	if raw, ok := resp.Data.Limits["audit_export_bytes"]; ok {
		_ = json.Unmarshal(raw, &maxBytes)
	}
	if maxBytes != int64(auditExportMaxBytes) {
		t.Fatalf("limits.audit_export_bytes = %d, want %d", maxBytes, auditExportMaxBytes)
	}

	// 5. The audit export says out loud why it has no column projection: this is the constraint a client would
	// otherwise assume away.
	if len(resp.Data.AuditExport.Notes) == 0 {
		t.Fatal("the audit export documents no constraints")
	}
	found := false
	for _, note := range resp.Data.AuditExport.Notes {
		if containsFold(note, "no column projection") {
			found = true
		}
	}
	if !found {
		t.Fatalf("the audit export does not explain the missing projection: %v", resp.Data.AuditExport.Notes)
	}
	if len(resp.Data.AuditExport.Fields) != 0 {
		t.Fatalf("the audit export advertises selectable fields: %v", resp.Data.AuditExport.Fields)
	}

	// 6. The output download advertises range support, because that is what a resuming client needs to know.
	if !resp.Data.Output.SupportsRanges {
		t.Fatal("the output download does not advertise range support")
	}

	// 7. Every entry names the path it describes, so the document is usable without guessing.
	for name, entry := range map[string]exportCapabilities{
		"task_export": resp.Data.TaskExport, "audit_export": resp.Data.AuditExport,
		"task_output_download": resp.Data.Output, "plugins_index": resp.Data.PluginsIndex,
	} {
		if entry.Path == "" {
			t.Fatalf("%s has no path", name)
		}
	}
}
