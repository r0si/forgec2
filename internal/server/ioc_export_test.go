package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

func seedIOCAgent(t *testing.T, s *Server, id string) {
	t.Helper()
	agent := db.Implant{ID: id, TenantID: 1, Hostname: "ioc-host", Status: "online", LastSeen: time.Now()}
	if err := s.db.Create(&agent).Error; err != nil {
		t.Fatal(err)
	}
}

// TestHandleListIOCs covers the extraction endpoint: indicators found in task
// output are returned, the type filter narrows them, and a bad `days` value
// falls back to the default instead of erroring.
func TestHandleListIOCs(t *testing.T) {
	s := mustTenantServer(t)
	seedIOCAgent(t, s, "11111111-1111-1111-1111-111111111111")

	task := db.Task{
		AgentID: "11111111-1111-1111-1111-111111111111",
		Type:    "shell",
		Status:  "completed",
		Result:  "c2 at https://evil.example/dropper reached 8.8.8.8 and www.bank.example",
	}
	if err := s.db.Create(&task).Error; err != nil {
		t.Fatal(err)
	}

	c, w := iocCtx(t, http.MethodGet, "/api/ioc?days=30&include_private=true", nil)
	s.handleListIOCs(c)
	if w.Code != 200 {
		t.Fatalf("list = %d %s", w.Code, w.Body.String())
	}
	var out struct {
		IOCs []struct {
			Type  string `json:"type"`
			Value string `json:"value"`
			Count int    `json:"count"`
		} `json:"iocs"`
		Total        int `json:"total"`
		TasksScanned int `json:"tasks_scanned"`
	}
	unmarshalData(t, w.Body.Bytes(), &out)
	if out.TasksScanned < 1 {
		t.Fatalf("tasks scanned = %d", out.TasksScanned)
	}
	if len(out.IOCs) == 0 {
		t.Fatalf("no indicators were extracted from the task output")
	}
	if out.Total != len(out.IOCs) {
		t.Fatalf("total = %d, len(iocs) = %d", out.Total, len(out.IOCs))
	}

	// A type filter narrows the list and never widens it.
	var seenType string
	for _, e := range out.IOCs {
		seenType = e.Type
		break
	}
	c, w = iocCtx(t, http.MethodGet, "/api/ioc?days=30&include_private=true&type="+seenType, nil)
	s.handleListIOCs(c)
	if w.Code != 200 {
		t.Fatalf("filtered list = %d", w.Code)
	}
	var filtered struct {
		IOCs []struct {
			Type string `json:"type"`
		} `json:"iocs"`
	}
	unmarshalData(t, w.Body.Bytes(), &filtered)
	for _, e := range filtered.IOCs {
		if e.Type != seenType {
			t.Fatalf("filter returned %q while filtering %q", e.Type, seenType)
		}
	}

	// A nonsense `days` falls back to the default rather than failing.
	c, w = iocCtx(t, http.MethodGet, "/api/ioc?days=not-a-number", nil)
	s.handleListIOCs(c)
	if w.Code != 200 {
		t.Fatalf("bad days = %d", w.Code)
	}
}

// TestHandleExportIOCs covers both export shapes and the CSV injection guard. A
// value that starts with =, +, - or @ is a spreadsheet formula when opened in
// Excel, so it must be quoted.
func TestHandleExportIOCs(t *testing.T) {
	s := mustTenantServer(t)
	seedIOCAgent(t, s, "22222222-2222-2222-2222-222222222222")

	task := db.Task{
		AgentID: "22222222-2222-2222-2222-222222222222",
		Type:    "shell",
		Status:  "completed",
		Result:  "hit 1.1.1.1 and 9.9.9.9",
	}
	if err := s.db.Create(&task).Error; err != nil {
		t.Fatal(err)
	}

	// CSV: header present, values present, attachment header set.
	c, w := iocCtx(t, http.MethodGet, "/api/ioc/export?format=csv&include_private=true", nil)
	s.handleExportIOCs(c)
	if w.Code != 200 {
		t.Fatalf("csv export = %d %s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/csv") {
		t.Fatalf("csv content type = %q", ct)
	}
	if cd := w.Header().Get("Content-Disposition"); !strings.Contains(cd, "attachment") || !strings.Contains(cd, ".csv") {
		t.Fatalf("csv disposition = %q", cd)
	}
	body := w.Body.String()
	if !strings.HasPrefix(body, "type,value,count,first_seen,last_seen") {
		t.Fatalf("csv header = %q", strings.SplitN(body, "\n", 2)[0])
	}
	if !strings.Contains(body, "1.1.1.1") {
		t.Fatalf("csv body missing the indicator: %s", body)
	}

	// STIX 2.1: a valid bundle with the documented marking definition.
	c, w = iocCtx(t, http.MethodGet, "/api/ioc/export?format=stix2&include_private=true", nil)
	s.handleExportIOCs(c)
	if w.Code != 200 {
		t.Fatalf("stix export = %d %s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "stix+json") {
		t.Fatalf("stix content type = %q", ct)
	}
	var bundle struct {
		Type    string `json:"type"`
		ID      string `json:"id"`
		Objects []struct {
			Type string `json:"type"`
			ID   string `json:"id"`
		} `json:"objects"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &bundle); err != nil {
		t.Fatalf("stix bundle is not JSON: %v", err)
	}
	if bundle.Type != "bundle" || !strings.HasPrefix(bundle.ID, "bundle--") {
		t.Fatalf("stix bundle = %+v", bundle)
	}
	sawIndicator := false
	sawMarking := false
	for _, o := range bundle.Objects {
		switch o.Type {
		case "indicator":
			sawIndicator = true
			if !strings.HasPrefix(o.ID, "indicator--") {
				t.Fatalf("indicator id = %q", o.ID)
			}
		case "marking-definition":
			sawMarking = true
		}
	}
	if !sawIndicator || !sawMarking {
		t.Fatalf("stix bundle missing objects: %+v", bundle.Objects)
	}
}

// TestCSVSafeQuotesFormulaLeaders pins the spreadsheet-injection guard used by
// the CSV export.
func TestCSVSafeQuotesFormulaLeaders(t *testing.T) {
	for _, dangerous := range []string{"=1+1", "+A1", "-2", "@SUM(A1)", "=cmd|' /C calc'!A0"} {
		got := csvSafe(dangerous)
		if got == dangerous {
			t.Errorf("csvSafe(%q) left the value untouched", dangerous)
		}
		if !strings.HasPrefix(got, "'") {
			t.Errorf("csvSafe(%q) = %q, want a leading quote", dangerous, got)
		}
	}
	// Ordinary values pass through untouched.
	for _, safe := range []string{"1.1.1.1", "evil.example", "a,b"} {
		if got := csvSafe(safe); got != safe {
			t.Errorf("csvSafe(%q) = %q, want it unchanged", safe, got)
		}
	}
}

func iocCtx(t *testing.T, method, target string, _ []byte) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(method, target, nil)
	c.Set("user", "admin1")
	c.Set("user_role", db.RoleAdmin)
	c.Set("user_id", uint(1))
	return c, w
}
