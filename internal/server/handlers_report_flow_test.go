package server

import (
	"net/http"
	"strings"
	"testing"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

// seedReportFixture builds a tenant-1 dataset plus a tenant-2 row that must
// stay out of every section.
func seedReportFixture(t *testing.T, s *Server) {
	t.Helper()
	if err := s.db.Create(&db.User{Username: "admin", Role: db.RoleAdmin, TenantID: 1, IsActive: true}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Create(&db.User{Username: "mallory", Role: db.RoleAdmin, TenantID: 2, IsActive: true}).Error; err != nil {
		t.Fatal(err)
	}
	agents := []db.Implant{
		{ID: "rep-a", TenantID: 1, Hostname: "rep-host-a", OS: "windows", IP: "10.0.0.1", Status: "online"},
		{ID: "rep-b", TenantID: 1, Hostname: "rep-host-b", OS: "linux", IP: "10.0.0.2", Status: "offline"},
		{ID: "rep-foreign", TenantID: 2, Hostname: "foreign-host", Status: "online"},
	}
	for i := range agents {
		if err := s.db.Create(&agents[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	tasks := []db.Task{
		{AgentID: "rep-a", TenantID: 1, Type: "shell", Command: "whoami", Status: "completed"},
		{AgentID: "rep-b", TenantID: 1, Type: "ps", Status: "completed"},
		{AgentID: "rep-a", TenantID: 1, Type: "shell", Command: "net user", Status: "failed"},
		{AgentID: "rep-foreign", TenantID: 2, Type: "shell", Command: "foreign", Status: "completed"},
	}
	for i := range tasks {
		if err := s.db.Create(&tasks[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	s.db.Create(&db.CredentialEntry{AgentID: "rep-a", TenantID: 1, Username: "alice", Type: "password"})
	s.db.Create(&db.CredentialEntry{AgentID: "rep-foreign", TenantID: 2, Username: "mallory"})
	s.db.Create(&db.AuditLog{User: "admin", Action: "login", Details: "signed in", Success: true})
	s.db.Create(&db.AuditLog{User: "mallory", Action: "login", Details: "foreign", Success: true})
}

func TestHandleGenerateReportJSON(t *testing.T) {
	s := mustTenantServer(t)
	seedReportFixture(t, s)

	body := `{"include":{"agents":true,"tasks":true,"creds":true,"audit":true},"format":"json"}`
	c, w := missingAPICtx(http.MethodPost, "/report/generate", body, nil)
	s.handleGenerateReport(c)
	if w.Code != http.StatusOK {
		t.Fatalf("report = %d %s", w.Code, w.Body.String())
	}
	var report struct {
		Title   string `json:"title"`
		Partial bool   `json:"partial"`
		Summary struct {
			TotalAgents int64  `json:"total_agents"`
			TotalTasks  int64  `json:"total_tasks"`
			TotalCreds  int64  `json:"total_creds"`
			TotalAudits int64  `json:"total_audits"`
			SuccessRate string `json:"success_rate"`
		} `json:"summary"`
		Agents []struct {
			Hostname string `json:"hostname"`
		} `json:"agents"`
		Tasks []struct {
			Command string `json:"command"`
		} `json:"tasks"`
		Credentials []struct {
			Username string `json:"username"`
		} `json:"credentials"`
		Audit []struct {
			User string `json:"user"`
		} `json:"audit"`
	}
	unmarshalData(t, w.Body.Bytes(), &report)
	if report.Title != "ForgeC2 Action Report" {
		t.Fatalf("title = %q", report.Title)
	}
	if report.Partial {
		t.Fatal("report marked partial on a healthy database")
	}
	// Tenant-1 agents/tasks/creds/audits only: the foreign row is excluded.
	if report.Summary.TotalAgents != 2 || report.Summary.TotalTasks != 3 {
		t.Fatalf("summary counts = %+v", report.Summary)
	}
	if report.Summary.TotalCreds != 1 || report.Summary.TotalAudits != 1 {
		t.Fatalf("summary creds/audits = %+v", report.Summary)
	}
	// 2 completed / (2 completed + 1 failed) = 66.7%, not a fabricated 100%.
	if report.Summary.SuccessRate != "66.7%" {
		t.Fatalf("success_rate = %q", report.Summary.SuccessRate)
	}
	if len(report.Agents) != 2 || len(report.Tasks) != 3 {
		t.Fatalf("lists = %d agents, %d tasks", len(report.Agents), len(report.Tasks))
	}
	for _, a := range report.Agents {
		if strings.Contains(a.Hostname, "foreign") {
			t.Fatalf("foreign agent in the report: %+v", report.Agents)
		}
	}
	if len(report.Credentials) != 1 || report.Credentials[0].Username != "alice" {
		t.Fatalf("credentials = %+v", report.Credentials)
	}
	if len(report.Audit) != 1 || report.Audit[0].User != "admin" {
		t.Fatalf("audit = %+v", report.Audit)
	}
}

func TestHandleGenerateReportHTML(t *testing.T) {
	s := mustTenantServer(t)
	seedReportFixture(t, s)

	c, w := missingAPICtx(http.MethodPost, "/report/generate",
		`{"include":{"agents":true,"tasks":true},"format":"html"}`, nil)
	s.handleGenerateReport(c)
	if w.Code != http.StatusOK {
		t.Fatalf("html report = %d %s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("content type = %q", ct)
	}
	body := w.Body.String()
	for _, want := range []string{"ForgeC2 Action Report", "rep-host-a", "whoami"} {
		if !strings.Contains(body, want) {
			t.Fatalf("html report missing %q", want)
		}
	}
}

func TestHandleGenerateReportValidation(t *testing.T) {
	s := mustTenantServer(t)
	seedReportFixture(t, s)

	cases := []struct {
		name string
		body string
		want int
	}{
		{"invalid format", `{"format":"pdf"}`, http.StatusBadRequest},
		{"start after end", `{"start_date":"2026-03-01","end_date":"2026-02-01"}`, http.StatusBadRequest},
		{"range too wide", `{"start_date":"2024-01-01","end_date":"2026-01-01"}`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, w := missingAPICtx(http.MethodPost, "/report/generate", tc.body, nil)
			s.handleGenerateReport(c)
			if w.Code != tc.want {
				t.Fatalf("status = %d, want %d (%s)", w.Code, tc.want, w.Body.String())
			}
		})
	}

	// An empty body means "defaults": last month, HTML.
	c, w := missingAPICtx(http.MethodPost, "/report/generate", `{}`, nil)
	s.handleGenerateReport(c)
	if w.Code != http.StatusOK {
		t.Fatalf("default report = %d %s", w.Code, w.Body.String())
	}

	// The report page itself renders (JSON for API clients).
	c, w = missingAPICtx(http.MethodGet, "/report", "", nil)
	s.handleReportPage(c)
	if w.Code != http.StatusOK {
		t.Fatalf("report page = %d %s", w.Code, w.Body.String())
	}
	if strings.TrimSpace(w.Body.String()) == "" {
		t.Fatal("report page returned an empty body")
	}
}

var _ = gin.Params{}
