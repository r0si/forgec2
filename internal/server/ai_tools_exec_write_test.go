package server

import (
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/forgec2/forgec2/internal/db"
)

func seedToolExecAgent(t *testing.T, s *Server, id string, tenant uint) {
	t.Helper()
	if err := s.db.Create(&db.Implant{ID: id, TenantID: tenant, Hostname: id, Status: "online"}).Error; err != nil {
		t.Fatalf("seed agent %s: %v", id, err)
	}
}

func TestAIToolExecuteCommand(t *testing.T) {
	s := setupToolExecServer(t)
	seedToolExecAgent(t, s, "exec-agent", 1)
	ctx := tenantOperatorCtx()

	// allow_execute off: the task is still created, but parked for approval.
	out := s.executeToolSwitchCtx(ctx, "execute_command", `{"agent_id":"exec-agent","command":"whoami","wait_for_result":false}`)
	if !strings.Contains(out, "pending_approval") {
		t.Fatalf("approval path = %s", out)
	}
	var created int64
	s.db.Model(&db.Task{}).Where("agent_id = ? AND type = ?", "exec-agent", "shell").Count(&created)
	if created != 1 {
		t.Fatalf("task rows = %d, want 1", created)
	}

	if out := s.executeToolSwitchCtx(ctx, "execute_command", `{"agent_id":"ghost","command":"whoami"}`); !strings.Contains(out, "agent not found") {
		t.Fatalf("unknown agent = %s", out)
	}

	// Sensitive commands stay approval-gated even with allow_execute on.
	s.cfg.AI.AllowExecute = true
	out = s.executeToolSwitchCtx(ctx, "execute_command", `{"agent_id":"exec-agent","command":"mimikatz sekurlsa logonpasswords","wait_for_result":false}`)
	if !strings.Contains(out, "sensitive_command") || !strings.Contains(out, "pending_approval") {
		t.Fatalf("sensitive command = %s", out)
	}

	// A quiet command with allow_execute on is queued directly.
	out = s.executeToolSwitchCtx(ctx, "execute_command", `{"agent_id":"exec-agent","command":"whoami /all","wait_for_result":false}`)
	var queued struct {
		TaskID uint   `json:"task_id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal([]byte(out), &queued); err != nil {
		t.Fatalf("decode queued command: %v (%s)", err, out)
	}
	if queued.TaskID == 0 || queued.Status == "" {
		t.Fatalf("queued = %+v", queued)
	}
}

func TestAIToolExecuteCommandBulk(t *testing.T) {
	s := setupToolExecServer(t)
	seedToolExecAgent(t, s, "bulk-a", 1)
	seedToolExecAgent(t, s, "bulk-b", 1)
	ctx := tenantOperatorCtx()

	if out := s.executeToolSwitchCtx(ctx, "execute_command_bulk", `{"agent_ids":[],"command":"whoami"}`); !strings.Contains(out, "agent_ids and command required") {
		t.Fatalf("missing args = %s", out)
	}

	out := s.executeToolSwitchCtx(ctx, "execute_command_bulk",
		`{"agent_ids":["bulk-a","ghost","bulk-b"],"command":"whoami","shell":"powershell.exe"}`)
	var res struct {
		Status  string `json:"status"`
		Results []struct {
			AgentID string `json:"agent_id"`
			TaskID  uint   `json:"task_id"`
			Error   string `json:"error"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("decode bulk: %v (%s)", err, out)
	}
	if len(res.Results) != 3 {
		t.Fatalf("bulk results = %+v", res.Results)
	}
	foundTask, foundErr := 0, 0
	for _, r := range res.Results {
		if r.AgentID == "ghost" {
			if r.Error == "" {
				t.Fatalf("unknown agent not reported: %+v", r)
			}
			foundErr++
			continue
		}
		if r.TaskID == 0 {
			t.Fatalf("task missing for %s: %+v", r.AgentID, r)
		}
		foundTask++
	}
	if foundTask != 2 || foundErr != 1 {
		t.Fatalf("bulk outcome = %+v", res.Results)
	}
}

func TestAIToolRunMacro(t *testing.T) {
	s := setupToolExecServer(t)
	seedToolExecAgent(t, s, "macro-agent", 1)
	ctx := tenantOperatorCtx()

	if out := s.executeToolSwitchCtx(ctx, "run_macro", `{"agent_ids":["macro-agent"],"macro_id":1}`); !strings.Contains(out, "allow_execute is disabled") {
		t.Fatalf("disabled run_macro = %s", out)
	}

	s.cfg.AI.AllowExecute = true
	if out := s.executeToolSwitchCtx(ctx, "run_macro", `{"macro_id":1}`); !strings.Contains(out, "agent_ids required") {
		t.Fatalf("missing agents = %s", out)
	}
	if out := s.executeToolSwitchCtx(ctx, "run_macro", `{"agent_ids":["macro-agent"]}`); !strings.Contains(out, "macro_id or macro_name required") {
		t.Fatalf("missing macro = %s", out)
	}
	if out := s.executeToolSwitchCtx(ctx, "run_macro", `{"agent_ids":["macro-agent"],"macro_id":999}`); !strings.Contains(out, "macro not found") {
		t.Fatalf("unknown id = %s", out)
	}
	if out := s.executeToolSwitchCtx(ctx, "run_macro", `{"agent_ids":["macro-agent"],"macro_name":"nope"}`); !strings.Contains(out, "not found by name") {
		t.Fatalf("unknown name = %s", out)
	}

	s.db.Create(&db.CommandMacro{TenantID: 1, Name: "recon", Steps: `[{"type":"shell","command":"whoami"}]`})
	out := s.executeToolSwitchCtx(ctx, "run_macro", `{"agent_ids":["macro-agent","ghost"],"macro_name":"recon"}`)
	var res struct {
		Dispatched int    `json:"dispatched"`
		Macro      string `json:"macro"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("decode run_macro: %v (%s)", err, out)
	}
	if res.Macro != "recon" {
		t.Fatalf("macro name = %q", res.Macro)
	}
	// "ghost" is skipped; the reached agent is reported even if its steps fail.
	if res.Dispatched < 0 || res.Dispatched > 1 {
		t.Fatalf("dispatched = %d", res.Dispatched)
	}
}

func TestAIToolCreateAutomationRule(t *testing.T) {
	s := setupToolExecServer(t)
	ctx := tenantOperatorCtx()

	if out := s.executeToolSwitchCtx(ctx, "create_automation_rule", `{"name":"x","event_type":"agent.checkin","command":"whoami"}`); !strings.Contains(out, "allow_execute is disabled") {
		t.Fatalf("disabled rule create = %s", out)
	}

	s.cfg.AI.AllowExecute = true
	if out := s.executeToolSwitchCtx(ctx, "create_automation_rule", `{}`); !strings.Contains(out, "name and event_type required") {
		t.Fatalf("missing fields = %s", out)
	}
	if out := s.executeToolSwitchCtx(ctx, "create_automation_rule", `{"name":"x","event_type":"nope","command":"whoami"}`); !strings.Contains(out, "invalid event_type") {
		t.Fatalf("bad event = %s", out)
	}
	if out := s.executeToolSwitchCtx(ctx, "create_automation_rule", `{"name":"x","event_type":"agent.checkin"}`); !strings.Contains(out, "command or macro_id required") {
		t.Fatalf("no action = %s", out)
	}

	out := s.executeToolSwitchCtx(ctx, "create_automation_rule", `{"name":"on checkin","event_type":"agent.checkin","command":"whoami"}`)
	var created struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		EventType string `json:"event_type"`
	}
	if err := json.Unmarshal([]byte(out), &created); err != nil {
		t.Fatalf("decode rule: %v (%s)", err, out)
	}
	if created.ID == "" || created.EventType != "agent.checkin" {
		t.Fatalf("created = %+v", created)
	}
	var rule db.AutomationRule
	if err := s.db.Where("id = ?", created.ID).First(&rule).Error; err != nil {
		t.Fatalf("rule not persisted: %v", err)
	}
	if !strings.Contains(rule.Actions, `"type":"command"`) || !strings.Contains(rule.Actions, "whoami") {
		t.Fatalf("rule actions = %s", rule.Actions)
	}

	// A macro-backed rule records the run_macro action instead of a command.
	out = s.executeToolSwitchCtx(ctx, "create_automation_rule", `{"name":"via macro","event_type":"task.complete","macro_id":7}`)
	if err := json.Unmarshal([]byte(out), &created); err != nil {
		t.Fatalf("decode macro rule: %v (%s)", err, out)
	}
	if created.ID == "" {
		t.Fatalf("macro rule response had no id: %s", out)
	}
	// Two rules created back to back must not share an id.
	var idCount int64
	s.db.Model(&db.AutomationRule{}).Where("id = ?", created.ID).Count(&idCount)
	if idCount != 1 {
		t.Fatalf("macro rule id %q matches %d rows", created.ID, idCount)
	}

	// Rapid successive creations used to collide on a UnixNano primary key
	// (UNIQUE constraint failure) because the Windows clock is coarse; five in
	// a row must all land with distinct ids.
	seen := map[string]bool{created.ID: true}
	for i := 0; i < 4; i++ {
		out := s.executeToolSwitchCtx(ctx, "create_automation_rule",
			`{"name":"rapid `+strconv.Itoa(i)+`","event_type":"task.fail","command":"whoami"}`)
		var rapid struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal([]byte(out), &rapid); err != nil || rapid.ID == "" {
			t.Fatalf("rapid rule %d = %s", i, out)
		}
		if seen[rapid.ID] {
			t.Fatalf("duplicate rule id %q", rapid.ID)
		}
		seen[rapid.ID] = true
	}

	var macroRule db.AutomationRule
	if err := s.db.Where("name = ?", "via macro").First(&macroRule).Error; err != nil {
		t.Fatalf("macro rule not persisted: %v (response=%s)", err, out)
	}
	if !strings.Contains(macroRule.Actions, `"type":"run_macro"`) || !strings.Contains(macroRule.Actions, `"macro_id":7`) {
		t.Fatalf("macro rule actions = %s", macroRule.Actions)
	}
}

func TestAIToolSaveEngagementNote(t *testing.T) {
	s := setupToolExecServer(t)
	s.configPath = filepath.Join(t.TempDir(), "config.yaml")
	ctx := tenantOperatorCtx()

	if out := s.executeToolSwitchCtx(ctx, "save_engagement_note", `{}`); !strings.Contains(out, "note required") {
		t.Fatalf("empty note = %s", out)
	}

	out := s.executeToolSwitchCtx(ctx, "save_engagement_note", `{"note":"client has EDR on all hosts"}`)
	var saved struct {
		Saved      bool   `json:"saved"`
		Mode       string `json:"mode"`
		TotalChars int    `json:"total_chars"`
	}
	if err := json.Unmarshal([]byte(out), &saved); err != nil {
		t.Fatalf("decode note: %v (%s)", err, out)
	}
	if !saved.Saved || saved.TotalChars == 0 {
		t.Fatalf("append result = %+v", saved)
	}
	if !strings.Contains(s.cfg.AI.EngagementNotes, "client has EDR") {
		t.Fatalf("note not stored: %q", s.cfg.AI.EngagementNotes)
	}

	// Replace mode swaps the whole note.
	out = s.executeToolSwitchCtx(ctx, "save_engagement_note", `{"note":"only this","mode":"replace"}`)
	if !strings.Contains(out, `"saved":true`) {
		t.Fatalf("replace = %s", out)
	}
	if s.cfg.AI.EngagementNotes != "only this" {
		t.Fatalf("replace kept old content: %q", s.cfg.AI.EngagementNotes)
	}

	// Oversized replace is refused rather than truncated silently.
	big := strings.Repeat("n", aiMaxNotesLen+1)
	if out := s.executeToolSwitchCtx(ctx, "save_engagement_note", `{"note":"`+big+`","mode":"replace"}`); !strings.Contains(out, "too large") {
		t.Fatalf("oversized note = %s", out[:120])
	}
}

func TestAIToolListenerCRUD(t *testing.T) {
	s := setupToolExecServer(t)
	ctx := tenantOperatorCtx()

	if out := s.executeToolSwitchCtx(ctx, "create_listener", `{"scheme":"http","host":"0.0.0.0","port":8080}`); !strings.Contains(out, "allow_execute is disabled") {
		t.Fatalf("disabled create_listener = %s", out)
	}

	s.cfg.AI.AllowExecute = true
	if out := s.executeToolSwitchCtx(ctx, "create_listener", `{"scheme":"bogus","host":"0.0.0.0","port":8080}`); !strings.Contains(out, "error") {
		t.Fatalf("invalid scheme = %s", out)
	}
	if out := s.executeToolSwitchCtx(ctx, "create_listener", `{"scheme":"http","port":8080}`); !strings.Contains(out, "host required") {
		t.Fatalf("missing host = %s", out)
	}
	if out := s.executeToolSwitchCtx(ctx, "create_listener", `{"scheme":"dns","port":53}`); !strings.Contains(out, "dns_domain required") {
		t.Fatalf("dns without domain = %s", out)
	}

	out := s.executeToolSwitchCtx(ctx, "create_listener", `{"name":"ai-http","scheme":"http","host":"0.0.0.0","port":18080}`)
	var listener struct {
		ID     uint   `json:"id"`
		Name   string `json:"name"`
		Scheme string `json:"scheme"`
		Port   int    `json:"port"`
	}
	if err := json.Unmarshal([]byte(out), &listener); err != nil {
		t.Fatalf("decode listener: %v (%s)", err, out)
	}
	if listener.ID == 0 || listener.Name != "ai-http" || listener.Port != 18080 {
		t.Fatalf("created listener = %+v", listener)
	}
	var row db.Listener
	if err := s.db.Where("id = ?", listener.ID).First(&row).Error; err != nil {
		t.Fatalf("listener not persisted: %v", err)
	}
	if row.TenantID != 1 {
		t.Fatalf("listener tenant = %d, want the principal's", row.TenantID)
	}

	// A listener with agents attached cannot be deleted.
	seedToolExecAgent(t, s, "listener-agent", 1)
	if err := s.db.Model(&db.Implant{}).Where("id = ?", "listener-agent").Update("listener_id", listener.ID).Error; err != nil {
		t.Fatal(err)
	}
	if out := s.executeToolSwitchCtx(ctx, "delete_listener", `{"listener_id":`+jsonNumber(listener.ID)+`}`); !strings.Contains(out, "agents still reference") {
		t.Fatalf("delete with agents = %s", out)
	}

	if err := s.db.Model(&db.Implant{}).Where("id = ?", "listener-agent").Update("listener_id", 0).Error; err != nil {
		t.Fatal(err)
	}
	if out := s.executeToolSwitchCtx(ctx, "delete_listener", `{"listener_id":"`+jsonNumber(listener.ID)+`"}`); !strings.Contains(out, `"deleted":true`) {
		t.Fatalf("delete = %s", out)
	}
	if out := s.executeToolSwitchCtx(ctx, "delete_listener", `{"listener_id":0}`); !strings.Contains(out, "numeric listener_id required") {
		t.Fatalf("missing listener id = %s", out)
	}
}
