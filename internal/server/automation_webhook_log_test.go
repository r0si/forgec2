package server

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"testing"
)

// TestWebhookResponseIsLogged pins the fix for a silent dead rule.
//
// executeWebhook never inspected the response status, so a receiver answering 500 or 404 was treated
// exactly like a 200 and its body - where it says "signature mismatch" or "unknown event" - was never
// read. A rule pointing at an endpoint that had moved or started rejecting produced no symptom at all.
func TestWebhookResponseIsLogged(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	evt := Event{Type: EventTaskComplete, AgentID: "a-1", TenantID: 1}
	response := func(status int, body string) *http.Response {
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}
	}

	// A rejecting receiver is a warning that carries its own explanation.
	logWebhookResponse("https://hooks.example.test/incoming?token=super-secret", evt, response(500, `{"error":"signature mismatch"}`))
	out := logs.String()
	if !strings.Contains(out, "webhook rejected") {
		t.Fatalf("a 500 response was not reported: %q", out)
	}
	if !strings.Contains(out, "500") || !strings.Contains(out, "signature mismatch") {
		t.Fatalf("the log line lost the status or the body: %q", out)
	}
	if !strings.Contains(out, "hooks.example.test") {
		t.Fatalf("the log line does not identify the receiver: %q", out)
	}
	// The query string can carry a token; only the host may be logged.
	if strings.Contains(out, "super-secret") {
		t.Fatalf("the webhook URL's query string leaked into the log: %q", out)
	}

	// A 200 is routine and stays at debug.
	logs.Reset()
	logWebhookResponse("https://hooks.example.test/incoming", evt, response(200, "ok"))
	debug := logs.String()
	if !strings.Contains(debug, "webhook delivered") {
		t.Fatalf("a delivered webhook was not logged at debug: %q", debug)
	}

	// Guard the guard: the helper above is useless if executeWebhook stops calling it, and a unit
	// test on the helper alone cannot see that.
	source, err := os.ReadFile("automation.go")
	if err != nil {
		t.Fatalf("read automation.go: %v", err)
	}
	start := strings.Index(string(source), "func (s *Server) executeWebhook(")
	if start < 0 {
		t.Fatal("executeWebhook not found")
	}
	body := string(source[start:])
	if end := strings.Index(body[1:], "\nfunc "); end > 0 {
		body = body[:end+1]
	}
	if !strings.Contains(body, "logWebhookResponse(params.URL") {
		t.Fatal("executeWebhook no longer inspects the webhook response")
	}

	// A huge body is bounded before it reaches the log.
	logs.Reset()
	logWebhookResponse("https://hooks.example.test/incoming", evt, response(502, strings.Repeat("x", 5000)))
	if got := logs.String(); len(got) > 800 {
		t.Fatalf("the log line was not truncated (%d bytes): %q", len(got), got[:200])
	}
}
