package server

import (
	"encoding/csv"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/playbook"
	"github.com/forgec2/forgec2/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// TestPlaybookRunPersistence covers save, list and read for playbook runs.
//
// The state machine is tested exhaustively in internal/playbook; what this covers is the join between it and storage - that a run
// survives the round trip, that the stored State column agrees with the value the machine computes, and that the read view reports
// what may run next without storing a second copy of that answer.
func TestPlaybookRunPersistence(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)

	call := func(method, path string, tenant uint) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(method, path, nil)
		parts := strings.Split(strings.TrimPrefix(path, "/api/playbooks/runs"), "/")
		if len(parts) > 1 && parts[1] != "" {
			c.Params = gin.Params{{Key: "id", Value: parts[1]}}
		}
		c.Set("user", "op")
		c.Set("user_role", db.RoleAdmin)
		c.Set("user_id", uint(1))
		c.Set(middleware.TenantIDContextKey, tenant)
		if len(c.Params) > 0 {
			s.handleGetPlaybookRun(c)
		} else {
			s.handleListPlaybookRuns(c)
		}
		return w
	}

	// 1. A half-finished run is saved, and its stored state matches the machine.
	run := playbook.Run{Name: "AD path", Steps: []playbook.Step{
		{ID: "recon"},
		{ID: "exploit", Needs: []string{"recon"}},
		{ID: "exfil", Needs: []string{"exploit"}, Approval: true},
	}}
	if err := run.Normalise(); err != nil {
		t.Fatalf("normalise: %v", err)
	}
	if err := run.Start("recon"); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := run.Succeed("recon"); err != nil {
		t.Fatalf("succeed: %v", err)
	}
	record, err := s.savePlaybookRun(playbookTenantContext(1), run, "op")
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if record.State != "blocked" {
		t.Fatalf("stored state = %q, want blocked (the approval step is waiting)", record.State)
	}
	run.ID = uitoa(record.ID)
	if _, err := s.savePlaybookRun(playbookTenantContext(1), run, "op"); err != nil {
		t.Fatalf("re-save: %v", err)
	}

	// 2. The list reports progress without opening the run.
	list := call(http.MethodGet, "/api/playbooks/runs", 1)
	if list.Code != http.StatusOK {
		t.Fatalf("list = %d (%s)", list.Code, list.Body.String())
	}
	var listed struct {
		Data struct {
			Count int `json:"count"`
			Runs  []struct {
				ID           uint     `json:"id"`
				State        string   `json:"state"`
				Settled      int      `json:"settled"`
				Total        int      `json:"total"`
				BlockedSteps []string `json:"blocked_steps"`
				NextStep     string   `json:"next_step"`
			} `json:"runs"`
		} `json:"data"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if listed.Data.Count != 1 || listed.Data.Runs[0].Settled != 1 || listed.Data.Runs[0].Total != 3 {
		t.Fatalf("list = %+v", listed.Data)
	}
	// "What needs me now" is on the list, not only in the detail: a reader should not have to open every run to find out whether the
	// blocker is a person (an approval step) or a dependency.
	if len(listed.Data.Runs[0].BlockedSteps) != 1 || listed.Data.Runs[0].BlockedSteps[0] != "exfil" {
		t.Fatalf("blocked_steps = %v, want [exfil]", listed.Data.Runs[0].BlockedSteps)
	}
	if listed.Data.Runs[0].NextStep != "exploit" {
		t.Fatalf("next_step = %q, want exploit", listed.Data.Runs[0].NextStep)
	}

	// ?attention=1 keeps only the runs a person has to act on, so "which approvals are waiting" is one request rather than a client-side
	// filter that a caller will usually skip.
	attentionRecorder := httptest.NewRecorder()
	cAttention, _ := gin.CreateTestContext(attentionRecorder)
	cAttention.Request = httptest.NewRequest(http.MethodGet, "/api/playbooks/runs?attention=1", nil)
	cAttention.Set("user", "op")
	cAttention.Set("user_role", db.RoleAdmin)
	cAttention.Set("user_id", uint(1))
	cAttention.Set(middleware.TenantIDContextKey, uint(1))
	s.handleListPlaybookRuns(cAttention)
	var attention struct {
		Data struct {
			Count int `json:"count"`
			Runs  []struct {
				ID           uint     `json:"id"`
				BlockedSteps []string `json:"blocked_steps"`
			} `json:"runs"`
		} `json:"data"`
	}
	if err := json.Unmarshal(attentionRecorder.Body.Bytes(), &attention); err != nil {
		t.Fatalf("decode attention: %v", err)
	}
	if attention.Data.Count != 1 || len(attention.Data.Runs) != 1 {
		t.Fatalf("attention list = %+v, want the one waiting run", attention.Data.Runs)
	}
	if len(attention.Data.Runs[0].BlockedSteps) == 0 {
		t.Fatal("the attention list returned a run with nothing blocked")
	}

	// 3. The detail names what may run next, and nothing that is waiting for a human.
	detail := call(http.MethodGet, "/api/playbooks/runs/"+uitoa(record.ID), 1)
	if detail.Code != http.StatusOK {
		t.Fatalf("detail = %d (%s)", detail.Code, detail.Body.String())
	}
	var shown struct {
		Data struct {
			State   string          `json:"state"`
			Ready   []playbook.Step `json:"ready"`
			Settled int             `json:"settled"`
			Steps   []playbook.Step `json:"steps"`
		} `json:"data"`
	}
	if err := json.Unmarshal(detail.Body.Bytes(), &shown); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	if shown.Data.State != "blocked" {
		t.Fatalf("detail state = %q", shown.Data.State)
	}
	if len(shown.Data.Ready) != 1 || shown.Data.Ready[0].ID != "exploit" {
		t.Fatalf("ready = %+v, want [exploit]", shown.Data.Ready)
	}
	if len(shown.Data.Steps) != 3 {
		t.Fatalf("steps = %d, want the three", len(shown.Data.Steps))
	}

	// 4. Another tenant cannot see it.
	if other := call(http.MethodGet, "/api/playbooks/runs/"+uitoa(record.ID), 2); other.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant read = %d, want 404", other.Code)
	}
	if otherList := call(http.MethodGet, "/api/playbooks/runs", 2); otherList.Code != http.StatusOK {
		t.Fatalf("cross-tenant list = %d", otherList.Code)
	} else if !strings.Contains(otherList.Body.String(), `"count":0`) {
		t.Fatalf("tenant 2 sees another tenant's runs: %s", otherList.Body.String())
	}

	// 5. A malformed id is a 400, not a 500.
	if bad := call(http.MethodGet, "/api/playbooks/runs/notanumber", 1); bad.Code != http.StatusBadRequest {
		t.Fatalf("malformed id = %d, want 400", bad.Code)
	}
}

// playbookTenantContext builds a gin context bound to one tenant, for the storage helpers that take a context rather than a request.
func playbookTenantContext(tenant uint) *gin.Context {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Set("user", "op")
	c.Set("user_role", db.RoleAdmin)
	c.Set("user_id", uint(1))
	c.Set(middleware.TenantIDContextKey, tenant)
	return c
}

// TestPlaybookRunStalledStep covers the other way a checklist dies: a step was started and nobody closed it.
//
// The state machine is right to call such a run "running" - the step IS running - so nothing in the model is wrong, and nothing draws
// attention either. Measured from the run's updated_at with a twelve-hour grace, and counted by ?attention=1, for the same reason the
// task queue calls a beacon with an hour-old task stalled.
func TestPlaybookRunStalledStep(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)

	// A run whose only step is running, last touched 13 hours ago.
	fresh := playbook.Run{Name: "stalled", Steps: []playbook.Step{{ID: "exfil", Status: playbook.StatusRunning}}}
	if err := fresh.Normalise(); err != nil {
		t.Fatalf("normalise: %v", err)
	}
	record, err := s.savePlaybookRun(playbookTenantContext(1), fresh, "op")
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	old := time.Now().Add(-13 * time.Hour)
	if err := s.db.Model(&db.PlaybookRun{}).Where("id = ?", record.ID).Update("updated_at", old).Error; err != nil {
		t.Fatalf("age the run: %v", err)
	}

	// A second run, also running, touched a minute ago: the control that proves the grace is applied rather than the state alone.
	recent := playbook.Run{Name: "in progress", Steps: []playbook.Step{{ID: "recon", Status: playbook.StatusRunning}}}
	if err := recent.Normalise(); err != nil {
		t.Fatalf("normalise recent: %v", err)
	}
	if _, err := s.savePlaybookRun(playbookTenantContext(1), recent, "op"); err != nil {
		t.Fatalf("save recent: %v", err)
	}

	read := func(query string) map[uint]bool {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, query, nil)
		c.Set("user", "op")
		c.Set("user_role", db.RoleAdmin)
		c.Set("user_id", uint(1))
		c.Set(middleware.TenantIDContextKey, uint(1))
		s.handleListPlaybookRuns(c)
		if w.Code != http.StatusOK {
			t.Fatalf("%s = %d (%s)", query, w.Code, w.Body.String())
		}
		var resp struct {
			Data struct {
				GraceSeconds int `json:"stall_grace_seconds"`
				Runs         []struct {
					ID       uint     `json:"id"`
					Stalled  bool     `json:"stalled"`
					RunSteps []string `json:"running_steps"`
					NextStep string   `json:"next_step"`
				} `json:"runs"`
			} `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if resp.Data.GraceSeconds != int(playbookStallGrace.Seconds()) {
			t.Fatalf("grace = %d seconds", resp.Data.GraceSeconds)
		}
		out := map[uint]bool{}
		for _, run := range resp.Data.Runs {
			out[run.ID] = run.Stalled
			if run.ID == record.ID && len(run.RunSteps) != 1 {
				t.Fatalf("running_steps = %v, want the one started step", run.RunSteps)
			}
		}
		return out
	}

	all := read("/api/playbooks/runs")
	if !all[record.ID] {
		t.Fatalf("a step untouched for 13 hours is not reported stalled: %+v", all)
	}
	if stalled, present := all[record.ID+1]; present && stalled {
		t.Fatal("a run touched a minute ago was reported stalled")
	}

	// And the attention filter keeps a stalled run, which is the point of the flag.
	if attention := read("/api/playbooks/runs?attention=1"); !attention[record.ID] {
		t.Fatalf("the attention list dropped the stalled run: %+v", attention)
	}
}

// TestPlaybookRunsCSVKeepsTheThreeQuestions covers the export of the checklist list.
//
// The columns are the JSON's three answers flattened into a table: how far the run got, what is waiting for a person, and whether it has
// stalled. A handover that carries tasks.csv and audit.ndjson but not this leaves the receiver rebuilding "where the structured path stood"
// from screenshots.
func TestPlaybookRunsCSVKeepsTheThreeQuestions(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)

	run := playbook.Run{Name: "csv-run", Steps: []playbook.Step{
		{ID: "recon", Status: playbook.StatusSucceeded},
		{ID: "exploit", Needs: []string{"recon"}},
		{ID: "exfil", Needs: []string{"exploit"}, Approval: true, Status: playbook.StatusBlocked},
	}}
	if err := run.Normalise(); err != nil {
		t.Fatalf("normalise: %v", err)
	}
	if _, err := s.savePlaybookRun(playbookTenantContext(1), run, "op"); err != nil {
		t.Fatalf("save: %v", err)
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/playbooks/runs?format=csv", nil)
	c.Set("user", "op")
	c.Set("user_role", db.RoleAdmin)
	c.Set("user_id", uint(1))
	c.Set(middleware.TenantIDContextKey, uint(1))
	s.handleListPlaybookRuns(c)
	if w.Code != http.StatusOK {
		t.Fatalf("csv = %d (%s)", w.Code, w.Body.String())
	}
	if contentType := w.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "text/csv") {
		t.Fatalf("Content-Type = %q", contentType)
	}

	rows, err := csv.NewReader(strings.NewReader(w.Body.String())).ReadAll()
	if err != nil {
		t.Fatalf("csv: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want a header and the one run", len(rows))
	}
	header := strings.Join(rows[0], ",")
	for _, column := range []string{"Run ID", "Blocked steps", "Next step", "Stalled"} {
		if !strings.Contains(header, column) {
			t.Fatalf("header %q is missing %q", header, column)
		}
	}
	// The three answers, in the one row: 1/3 settled, exfil blocked on a person, exploit ready next, not stalled.
	row := rows[1]
	if row[3] != "op" {
		t.Fatalf("created by = %q", row[3])
	}
	if row[5] != "1" || row[6] != "3" {
		t.Fatalf("progress = %s/%s, want 1/3", row[5], row[6])
	}
	if row[7] != "exfil" {
		t.Fatalf("blocked steps = %q, want exfil", row[7])
	}
	if row[8] != "exploit" {
		t.Fatalf("next step = %q, want exploit", row[8])
	}
	if row[10] != "false" {
		t.Fatalf("stalled = %q, want false", row[10])
	}
}
