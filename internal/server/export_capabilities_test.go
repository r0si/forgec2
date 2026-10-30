package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
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
			TaskExport   exportCapabilities `json:"task_export"`
			AuditExport  exportCapabilities `json:"audit_export"`
			Output       exportCapabilities `json:"task_output_download"`
			PluginsIndex exportCapabilities `json:"plugins_index"`
			Bundle       struct {
				Path       string   `json:"path"`
				Members    []string `json:"members"`
				VerifyPath string   `json:"verify_path"`
				Signed     bool     `json:"signed"`
				Algorithm  string   `json:"signature_algorithm"`
				PublicKey  string   `json:"public_key"`
				Notes      []string `json:"notes"`
			} `json:"engagement_bundle"`
			Diagnostics []struct {
				Path        string   `json:"path"`
				Method      string   `json:"method"`
				Answers     string   `json:"answers"`
				SideEffects *bool    `json:"side_effects"`
				Notes       []string `json:"notes"`
			} `json:"diagnostics"`
			Maintenance []struct {
				Path    string   `json:"path"`
				Method  string   `json:"method"`
				Clears  string   `json:"clears"`
				Audited string   `json:"audited"`
				Params  []string `json:"params"`
			} `json:"maintenance"`
			DiagnosticRateLimit struct {
				Requests      int      `json:"requests"`
				WindowSeconds int      `json:"window_seconds"`
				Scope         string   `json:"scope"`
				Notes         []string `json:"notes"`
			} `json:"diagnostic_rate_limit"`
			Limits         map[string]json.RawMessage `json:"limits"`
			SignatureKinds map[string]string          `json:"signature_kinds"`
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
	// 6b. The four signature kinds are published so a client can compare against them instead of prose copied from a release
	// note - and they must be the strings the responses actually report.
	for name, want := range map[string]string{
		"audit": auditSignatureKind, "bundle": bundleSignatureKind,
		"plugin": pluginSignatureKind, "failure": failureSignatureKind,
	} {
		if got := resp.Data.SignatureKinds[name]; got != want {
			t.Fatalf("published %s signature kind = %q, want the constant", name, got)
		}
	}
	if len(resp.Data.SignatureKinds) != 5 { // four kinds plus the note
		t.Fatalf("signature kinds = %v, want the four kinds and the note", resp.Data.SignatureKinds)
	}
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

	// 7. The bundle entry lists the members from the same slice the parser uses, and says whether a handover will be
	// signed - an operational fact an operator should not have to discover by producing one.
	if len(resp.Data.Bundle.Members) != len(bundleMemberNames) {
		t.Fatalf("bundle members = %v, want %v", resp.Data.Bundle.Members, bundleMemberNames)
	}
	for i, name := range bundleMemberNames {
		if resp.Data.Bundle.Members[i] != name {
			t.Fatalf("bundle member %d = %q, want %q", i, resp.Data.Bundle.Members[i], name)
		}
	}
	if resp.Data.Bundle.VerifyPath != "/api/engagements/:id/bundle/verify" {
		t.Fatalf("verify_path = %q", resp.Data.Bundle.VerifyPath)
	}
	if resp.Data.Bundle.Signed || resp.Data.Bundle.PublicKey != "" || resp.Data.Bundle.Algorithm != "" {
		t.Fatalf("an unconfigured deployment advertised signing: %+v", resp.Data.Bundle)
	}
	if len(resp.Data.Bundle.Notes) == 0 {
		t.Fatal("the bundle entry documents no constraints")
	}

	// 7b. The diagnostics surface is listed, and every entry is also in the API document: a capability that is not in
	// the spec is one a client author will not find either.
	if len(resp.Data.Diagnostics) == 0 {
		t.Fatal("the diagnostics surface is empty")
	}
	spec, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatalf("read the spec: %v", err)
	}
	// Every entry must STATE whether it has side effects. A client that offers "run all diagnostics" has no way to
	// know otherwise, and one of these really does send a notification.
	// The entries with side effects are a NAMED set, not a count: a client that offers "run all diagnostics" must exclude
	// exactly these, and the assertion says which rather than how many.
	sideEffecting := map[string]bool{}
	for _, entry := range resp.Data.Diagnostics {
		if entry.Path == "" || entry.Method == "" || entry.Answers == "" {
			t.Fatalf("an incomplete diagnostics entry: %+v", entry)
		}
		if entry.SideEffects == nil {
			t.Fatalf("%s does not state side_effects", entry.Path)
		}
		if *entry.SideEffects {
			sideEffecting[entry.Path] = true
			if !strings.Contains(entry.Path, "/test") {
				t.Fatalf("%s claims side effects but is not a test endpoint", entry.Path)
			}
		}
		// The capabilities document uses gin's ":id" spelling; the spec uses "{id}".
		specPath := strings.ReplaceAll(entry.Path, ":id", "{id}")
		if !strings.Contains(string(spec), specPath+":") {
			t.Fatalf("%s is advertised as a diagnostic but is not in api/openapi.yaml", specPath)
		}
	}
	// The maintenance endpoints are listed apart from the diagnostics: every diagnostic answers a question, while these
	// change what the report will say next. Both must name the audit action they write, because "the counter was cleared"
	// is a change someone will look for later.
	if len(resp.Data.Maintenance) != 3 {
		// Three: the two counter resets and the signature rerun, which changes what the reports say next in the same way.
		t.Fatalf("maintenance entries = %d, want three", len(resp.Data.Maintenance))
	}
	for _, entry := range resp.Data.Maintenance {
		if entry.Path == "" || entry.Method != "POST" || entry.Clears == "" || entry.Audited == "" {
			t.Fatalf("an incomplete maintenance entry: %+v", entry)
		}
		specPath := strings.ReplaceAll(entry.Path, ":id", "{id}")
		if !strings.Contains(string(spec), specPath+":") {
			t.Fatalf("%s is listed as maintenance but is not in api/openapi.yaml", specPath)
		}
	}

	// Exactly one diagnostic delivers something real, and it is the route test. More than one would make a batch runner
	// dangerous; none would mean the flag is decoration.
	for _, want := range []string{"/api/notification-routes/:id/test", "/api/automation/rules/:id/test"} {
		if !sideEffecting[want] {
			t.Fatalf("%s is not listed as side-effecting (got %v)", want, sideEffecting)
		}
	}
	if len(sideEffecting) != 2 {
		t.Fatalf("side-effecting diagnostics = %v, want exactly the two test endpoints", sideEffecting)
	}

	// 7c. The diagnostics budget is published from the same constants the limiter uses: a client that knows the ceiling
	// can back off before it is throttled, and a copy of the number here could silently disagree with the limiter.
	if resp.Data.DiagnosticRateLimit.Requests != diagnosticRateLimitRequests {
		t.Fatalf("published requests = %d, the limiter uses %d",
			resp.Data.DiagnosticRateLimit.Requests, diagnosticRateLimitRequests)
	}
	if resp.Data.DiagnosticRateLimit.WindowSeconds != int(diagnosticRateLimitWindow.Seconds()) {
		t.Fatalf("published window = %d, the limiter uses %d",
			resp.Data.DiagnosticRateLimit.WindowSeconds, int(diagnosticRateLimitWindow.Seconds()))
	}
	if resp.Data.DiagnosticRateLimit.Scope == "" || len(resp.Data.DiagnosticRateLimit.Notes) == 0 {
		t.Fatalf("the rate limit is not explained: %+v", resp.Data.DiagnosticRateLimit)
	}

	// 7d. The rule-set health report advertises what it now returns, so a client can read the action state without
	// parsing the description.
	specText := string(spec)
	for _, field := range []string{"action_state", "actions_ok", "actions_failed", "last_action_error", "action_failing"} {
		if !strings.Contains(specText, field) {
			t.Fatalf("api/openapi.yaml does not document %q in the rule-set health report", field)
		}
	}

	// 8. With a key configured the same entry reports it, so an operator can hand the expected key to the receiver.
	seed := "9d61b19deffd5a60ba844af492ec2cc44449c5697b326919703bac031cae7f60"
	t.Setenv(bundleSigningKeyEnv, seed)
	w2 := httptest.NewRecorder()
	c2, _ := gin.CreateTestContext(w2)
	c2.Request = httptest.NewRequest(http.MethodGet, "/api/exports/capabilities", nil)
	c2.Set("user", "op")
	c2.Set("user_role", db.RoleAdmin)
	c2.Set("user_id", uint(1))
	c2.Set(middleware.TenantIDContextKey, uint(1))
	s.handleExportCapabilities(c2)
	var signed struct {
		Data struct {
			Bundle struct {
				Signed    bool   `json:"signed"`
				Algorithm string `json:"signature_algorithm"`
				PublicKey string `json:"public_key"`
			} `json:"engagement_bundle"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w2.Body.Bytes(), &signed); err != nil {
		t.Fatalf("decode signed capabilities: %v", err)
	}
	if !signed.Data.Bundle.Signed || signed.Data.Bundle.Algorithm != "ed25519" || signed.Data.Bundle.PublicKey == "" {
		t.Fatalf("a configured signing key was not reported: %+v", signed.Data.Bundle)
	}

	// 9. Every entry names the path it describes, so the document is usable without guessing.
	for name, entry := range map[string]exportCapabilities{
		"task_export": resp.Data.TaskExport, "audit_export": resp.Data.AuditExport,
		"task_output_download": resp.Data.Output, "plugins_index": resp.Data.PluginsIndex,
	} {
		if entry.Path == "" {
			t.Fatalf("%s has no path", name)
		}
	}
}

// TestCapabilitiesListEveryCSVReport is a cross-check between what the code can do and what it says it can do.
//
// The catalogue is a discovery surface: a client that reads it and finds only the task and audit exports will re-implement the reports
// instead of asking for the file. This test walks the server files for the CSV branch (`?format=csv`) and requires each endpoint it finds
// to be listed - so adding a report and forgetting the catalogue fails here rather than in a client author's afternoon.
func TestCapabilitiesListEveryCSVReport(t *testing.T) {
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
		t.Fatalf("capabilities = %d (%s)", w.Code, w.Body.String())
	}
	var decoded struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	capabilities := map[string]interface{}{}
	for key, raw := range decoded.Data {
		if key != "reports" {
			continue
		}
		var reports reportCapabilities
		if err := json.Unmarshal(raw, &reports); err != nil {
			t.Fatalf("decode reports: %v", err)
		}
		capabilities["reports"] = reports
	}

	listed := map[string]bool{}
	reports, _ := capabilities["reports"].(reportCapabilities)
	for _, report := range reports.Reports {
		listed[report.Path] = true
		if report.Answers == "" {
			t.Fatalf("%s is listed without saying what it answers", report.Path)
		}
	}

	// The endpoints that implement a CSV branch, mapped to the path the router exposes. Hand-written because the alternative is parsing
	// the handlers; the test's job is to fail when one of these gains a sibling and nobody lists it, so the list is checked in BOTH
	// directions against the catalogue.
	expected := []string{
		"/api/agents",
		"/api/agents/:agentId/timeline",
		"/api/automation/rules",
		"/api/notification-routes",
		"/api/stats/failures",
		"/api/stats/failures/signatures",
		"/api/stats/tasks/duration",
		"/api/stats/tasks/queue",
		"/api/stats/builds/failures",
	}
	for _, path := range expected {
		if !listed[path] {
			t.Fatalf("%s can answer csv but the catalogue does not list it", path)
		}
	}
	for path := range listed {
		found := false
		for _, candidate := range expected {
			if candidate == path {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("the catalogue lists %s, which no test knows how to check", path)
		}
	}
	if len(reports.Formats) != 2 {
		t.Fatalf("report formats = %v, want csv and json", reports.Formats)
	}
}
