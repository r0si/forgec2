package server

import (
	"bytes"
	"context"
	"log/slog"

	"github.com/forgec2/forgec2/internal/db"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestRedactURLFromMessageHidesTheToken pins the redaction: a webhook URL is often
// the credential itself (a Slack incoming-webhook URL is), and a *url.Error
// stringifies the whole URL — so logging the raw delivery error wrote the token
// into the log.
func TestRedactURLFromMessageHidesTheToken(t *testing.T) {
	const secretURL = "https://hooks.example.com/services/T000/B000/SUPERSECRETTOKEN"
	msg := `Post "` + secretURL + `": dial tcp 10.0.0.1:443: connect: connection refused`

	got := redactURLFromMessage(msg, secretURL)
	if strings.Contains(got, "SUPERSECRETTOKEN") {
		t.Fatalf("the token survived redaction: %s", got)
	}
	if !strings.Contains(got, "hooks.example.com") {
		t.Fatalf("the host was dropped, the log is no longer diagnosable: %s", got)
	}
	// A message with query parameters is redacted too.
	q := "https://hooks.example.com/x?token=abc123"
	if out := redactURLFromMessage("failed: "+q, q); strings.Contains(out, "abc123") {
		t.Fatalf("a query token survived: %s", out)
	}
	// Inputs that do not mention the URL are untouched.
	if out := redactURLFromMessage("plain failure", secretURL); out != "plain failure" {
		t.Fatalf("an unrelated message was rewritten: %s", out)
	}
	if out := redactURLFromMessage("x", ""); out != "x" {
		t.Fatal("an empty URL changed the message")
	}
}

// TestRedactURLForLog covers the helper directly, including a URL that cannot be
// parsed (it must not echo the raw value).
func TestRedactURLForLog(t *testing.T) {
	if got := redactURLForLog("https://hooks.example.com/a/b/c"); !strings.Contains(got, "hooks.example.com") || strings.Contains(got, "/a/b/c") {
		t.Fatalf("redactURLForLog = %q", got)
	}
	if got := redactURLForLog("::not a url::"); strings.Contains(got, "not a url") {
		t.Fatalf("an unparseable URL was echoed: %q", got)
	}
	if got := redactURLForLog(""); got != "<webhook-url>" {
		t.Fatalf("empty URL = %q", got)
	}
}

// TestWebhookRetryDelayIsBounded pins the backoff lookup: raising
// webhookMaxRetries past the table length must not index out of range.
func TestWebhookRetryDelayIsBounded(t *testing.T) {
	for attempt := -1; attempt < len(webhookBackoffSteps)+5; attempt++ {
		if d := webhookRetryDelay(attempt); d <= 0 || d > 10*time.Second {
			t.Fatalf("webhookRetryDelay(%d) = %v", attempt, d)
		}
	}
	if webhookRetryDelay(0) != webhookBackoffSteps[0] {
		t.Fatal("the first delay does not match the table")
	}
	if webhookRetryDelay(99) != webhookBackoffSteps[len(webhookBackoffSteps)-1] {
		t.Fatal("an out-of-range attempt does not reuse the last step")
	}
}

// TestFireWebhookFailureDoesNotLogTheToken is the end-to-end guard: a delivery
// that fails must not write the webhook URL into the log.
func TestFireWebhookFailureDoesNotLogTheToken(t *testing.T) {
	s := mustTenantServer(t)
	// NewRequestWithContext panics on a nil Context, and the test helper does not
	// build one (NewServer does).
	s.ctx = context.Background()
	s.httpClient = &http.Client{Timeout: 300 * time.Millisecond}
	// Exercise the full retry path without waiting the real 1s+3s+7s backoff.
	prevBackoff := webhookBackoffSteps
	webhookBackoffSteps = []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}
	t.Cleanup(func() { webhookBackoffSteps = prevBackoff })

	// A public-looking target that refuses the connection (TEST-NET-3, port 9).
	// fireWebhook validates with validateWebhookURL (not the test seam), so the
	// URL has to pass the address policy to reach client.Do - a local receiver
	// would be rejected before delivery and the test would prove nothing.
	secretURL := "http://203.0.113.9:9/services/T000/SUPERSECRETTOKEN"

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	wh := db.WebhookConfig{Name: "token-probe", URL: secretURL, Method: "POST", EventType: string(EventImplantCheckin), Enabled: true}
	if err := s.fireWebhook(wh, Event{Type: EventImplantCheckin, AgentID: "a1", Timestamp: time.Now()}); err == nil {
		t.Fatal("delivery to a refusing target reported success")
	}

	logged := buf.String()
	if strings.Contains(logged, "SUPERSECRETTOKEN") {
		t.Fatalf("the webhook token was written to the log:\n%s", logged)
	}
	if logged == "" {
		t.Fatal("nothing was logged; the test cannot prove anything")
	}
	if !strings.Contains(logged, "203.0.113.9") {
		t.Fatalf("the host was dropped from the log, it is no longer diagnosable:\n%s", logged)
	}
}
