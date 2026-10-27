package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/pkg/protocol"
)

// callbackRecorder is a local receiver for task callbacks.
type callbackRecorder struct {
	mu     sync.Mutex
	bodies [][]byte
	header []http.Header
}

func newCallbackServer(t *testing.T, rec *callbackRecorder) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		rec.mu.Lock()
		rec.bodies = append(rec.bodies, body)
		rec.header = append(rec.header, r.Header.Clone())
		rec.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (r *callbackRecorder) snapshot() ([][]byte, []http.Header) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([][]byte{}, r.bodies...), append([]http.Header{}, r.header...)
}

// TestExecuteTaskCallbackDelivers pins the callback contract: the payload names
// the task and agent, carries the command and status, defaults the method to
// POST, and sends the file-scan result in its REDACTED form — a callback leaves
// the server, so matched secrets must not travel with it.
func TestExecuteTaskCallbackDelivers(t *testing.T) {
	s := mustTenantServer(t)
	s.httpClient = &http.Client{Timeout: 5 * time.Second}

	rec := &callbackRecorder{}
	srv := newCallbackServer(t, rec)

	// The SSRF gate is exercised by TestExecuteTaskCallbackRejectsPrivateTarget;
	// here the seam lets the delivery reach a local receiver.
	webhookTargetValidator = func(string) error { return nil }
	t.Cleanup(func() { webhookTargetValidator = validateWebhookURL })

	task := db.Task{
		ID:             7,
		AgentID:        "a1",
		Type:           protocol.TaskTypeFileScan,
		Command:        "scan C:\\",
		Status:         "completed",
		CallbackURL:    srv.URL,
		CallbackMethod: "",
		UpdatedAt:      time.Now(),
		Result:         `{"root":"C:\\","findings":[{"path":"C:\\k.txt","name":"k.txt","rule":"aws_key","value":"AKIA-SECRET","line":3,"redacted":false}]}`,
	}
	s.executeTaskCallback(task, "a1")

	bodies, headers := rec.snapshot()
	if len(bodies) != 1 {
		t.Fatalf("callbacks received = %d, want 1", len(bodies))
	}
	if got := headers[0].Get("Content-Type"); got != "application/json" {
		t.Fatalf("content type = %q", got)
	}
	if got := headers[0].Get("User-Agent"); got != "ForgeC2-Callback/1.0" {
		t.Fatalf("user agent = %q", got)
	}

	var payload struct {
		TaskID   uint   `json:"task_id"`
		AgentID  string `json:"agent_id"`
		Type     string `json:"type"`
		Command  string `json:"command"`
		Status   string `json:"status"`
		Result   string `json:"result"`
		Complete string `json:"completed"`
	}
	if err := json.Unmarshal(bodies[0], &payload); err != nil {
		t.Fatalf("decode payload: %v (body %s)", err, bodies[0])
	}
	if payload.TaskID != 7 || payload.AgentID != "a1" || payload.Command != "scan C:\\" || payload.Status != "completed" {
		t.Fatalf("payload = %+v", payload)
	}
	if payload.Complete == "" {
		t.Fatal("completed timestamp missing")
	}
	if payload.Result == "" {
		t.Fatal("result missing")
	}
	if containsSecret(payload.Result) {
		t.Fatalf("callback leaked the matched secret: %s", payload.Result)
	}
}

func containsSecret(s string) bool { return strings.Contains(s, "AKIA-SECRET") }

// TestExecuteTaskCallbackRejectsPrivateTarget pins the SSRF gate: a callback to
// a private address is refused and nothing is sent.
func TestExecuteTaskCallbackRejectsPrivateTarget(t *testing.T) {
	s := mustTenantServer(t)
	s.httpClient = &http.Client{Timeout: 2 * time.Second}

	rec := &callbackRecorder{}
	srv := newCallbackServer(t, rec)

	task := db.Task{ID: 8, AgentID: "a1", Type: "shell", Status: "completed", CallbackURL: srv.URL, UpdatedAt: time.Now(), Result: "x"}
	s.executeTaskCallback(task, "a1")

	bodies, _ := rec.snapshot()
	if len(bodies) != 0 {
		t.Fatalf("a callback to a private address was delivered (%d)", len(bodies))
	}
}
