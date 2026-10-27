package server

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/db"
)

// TestTaskCallbackLogsHideTheToken pins the same redaction on the task-callback
// path: the callback URL is operator-supplied and may carry a token, and the
// delivery error stringifies the whole URL.
func TestTaskCallbackLogsHideTheToken(t *testing.T) {
	s := mustTenantServer(t)
	s.ctx = context.Background()
	s.httpClient = &http.Client{Timeout: 300 * time.Millisecond}

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	const callbackURL = "http://203.0.113.9:9/hook/SUPERSECRETTOKEN"
	task := db.Task{
		ID: 11, AgentID: "agent-cb", Type: "shell", Status: "completed",
		Command: "whoami", CallbackURL: callbackURL, CallbackMethod: "POST",
		UpdatedAt: time.Now(), Result: "ok",
	}
	s.executeTaskCallback(task, "agent-cb")

	logged := buf.String()
	if logged == "" {
		t.Fatal("nothing was logged; the test cannot prove anything")
	}
	if strings.Contains(logged, "SUPERSECRETTOKEN") {
		t.Fatalf("the callback token was written to the log:\n%s", logged)
	}
	if !strings.Contains(logged, "203.0.113.9") {
		t.Fatalf("the host was dropped, the log is no longer diagnosable:\n%s", logged)
	}
}

// TestRedactURLFromMessageCoversProviderKeys pins the provider shape: Gemini-style
// endpoints carry the API key in the query string, so redaction must drop it while
// keeping the host.
func TestRedactURLFromMessageCoversProviderKeys(t *testing.T) {
	const providerURL = "https://generativelanguage.googleapis.com/v1/models:generate?key=AIzaSECRETKEY"
	out := redactURLForLog(providerURL)
	if strings.Contains(out, "AIzaSECRETKEY") {
		t.Fatalf("the provider key survived: %s", out)
	}
	if !strings.Contains(out, "generativelanguage.googleapis.com") {
		t.Fatalf("the host was dropped: %s", out)
	}
	msg := `Post "` + providerURL + `": context deadline exceeded`
	redacted := redactURLFromMessage(msg, providerURL)
	if strings.Contains(redacted, "AIzaSECRETKEY") {
		t.Fatalf("the key survived message redaction: %s", redacted)
	}
}
