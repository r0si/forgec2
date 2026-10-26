package server

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/google/uuid"
)

// TestAIRunTerminal pins which statuses count as finished.
func TestAIRunTerminal(t *testing.T) {
	for _, s := range []string{aiRunStatusCompleted, aiRunStatusFailed, aiRunStatusCancelled, aiRunStatusInterrupted} {
		if !aiRunTerminal(s) {
			t.Fatalf("%q not treated as terminal", s)
		}
	}
	for _, s := range []string{"running", "queued", ""} {
		if aiRunTerminal(s) {
			t.Fatalf("%q treated as terminal", s)
		}
	}
}

// TestAIRunCancel covers the operator cancel: the run and its pending intents
// are marked cancelled, a terminal run is returned untouched, and another
// owner cannot see the run at all.
func TestAIRunCancel(t *testing.T) {
	s := setupToolExecServer(t)
	me := seedAIUser(t, s, "ai-user", 1)
	run := seedAIRun(t, s, uuid.NewString(), 1, me.ID, "ai-user", "running")
	intent := db.AIExecutionIntent{
		ID: uuid.NewString(), TenantID: 1, OwnerID: me.ID, Owner: "ai-user",
		SessionID: 1, RunID: run.ID, ToolName: "execute_command", Arguments: `{}`,
		Status: "pending", Risk: "high", CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := s.db.Create(&intent).Error; err != nil {
		t.Fatal(err)
	}

	c, w := aiRunCtx("ai-user", db.RoleAdmin, http.MethodPost, "/api/ai/runs/"+run.ID+"/cancel", run.ID)
	s.handleAIRunCancel(c)
	if w.Code != http.StatusOK {
		t.Fatalf("cancel = %d %s", w.Code, w.Body.String())
	}
	var reloaded db.AIChatRun
	if err := s.db.Where("id = ?", run.ID).First(&reloaded).Error; err != nil {
		t.Fatal(err)
	}
	if reloaded.Status != aiRunStatusCancelled {
		t.Fatalf("run status = %q, want cancelled", reloaded.Status)
	}
	var reloadedIntent db.AIExecutionIntent
	if err := s.db.Where("id = ?", intent.ID).First(&reloadedIntent).Error; err != nil {
		t.Fatal(err)
	}
	if reloadedIntent.Status != "cancelled" || reloadedIntent.DecidedBy != "ai-user" {
		t.Fatalf("intent = %+v, want cancelled by the operator", reloadedIntent)
	}

	// Cancelling again is a no-op success (the run is already terminal).
	c, w = aiRunCtx("ai-user", db.RoleAdmin, http.MethodPost, "/api/ai/runs/"+run.ID+"/cancel", run.ID)
	s.handleAIRunCancel(c)
	if w.Code != http.StatusOK {
		t.Fatalf("second cancel = %d %s", w.Code, w.Body.String())
	}

	// Another owner cannot see the run at all.
	seedAIUser(t, s, "other-user", 1)
	c, w = aiRunCtx("other-user", db.RoleAdmin, http.MethodPost, "/api/ai/runs/"+run.ID+"/cancel", run.ID)
	s.handleAIRunCancel(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("foreign cancel = %d %s", w.Code, w.Body.String())
	}
}

// TestAIRunEventsReplay covers the SSE replay of a finished run: durable events
// are emitted in order, and Last-Event-ID skips what the client already has.
func TestAIRunEventsReplay(t *testing.T) {
	s := setupToolExecServer(t)
	me := seedAIUser(t, s, "ai-user", 1)
	run := seedAIRun(t, s, uuid.NewString(), 1, me.ID, "ai-user", aiRunStatusCompleted)
	for i, typ := range []string{"token", "tool_result"} {
		ev := db.AIChatRunEvent{RunID: run.ID, Sequence: int64(i + 1), Type: typ, Payload: `{"n":` + jsonNumber(uint(i)) + `}`, CreatedAt: time.Now()}
		if err := s.db.Create(&ev).Error; err != nil {
			t.Fatal(err)
		}
	}

	c, w := aiRunCtx("ai-user", db.RoleAdmin, http.MethodGet, "/api/ai/runs/"+run.ID+"/events", run.ID)
	s.handleAIRunEvents(c)
	if w.Code != http.StatusOK {
		t.Fatalf("events = %d %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "id: 1") || !strings.Contains(body, "event: token") {
		t.Fatalf("missing first event in stream: %.300s", body)
	}
	if !strings.Contains(body, "id: 2") || !strings.Contains(body, "event: tool_result") {
		t.Fatalf("missing second event in stream: %.300s", body)
	}
	if got := w.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
		t.Fatalf("content type = %q", got)
	}

	// Last-Event-ID resumes after the first event.
	c, w = aiRunCtx("ai-user", db.RoleAdmin, http.MethodGet, "/api/ai/runs/"+run.ID+"/events", run.ID)
	c.Request.Header.Set("Last-Event-ID", "1")
	s.handleAIRunEvents(c)
	body = w.Body.String()
	if strings.Contains(body, "id: 1") {
		t.Fatalf("replayed an event the client already had: %.200s", body)
	}
	if !strings.Contains(body, "id: 2") {
		t.Fatalf("missing the event after the cursor: %.200s", body)
	}
}
