package server

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/db"
)

// TestFlushAuditEntriesSanitizesDirectRows pins the bypass fix: callers that build
// db.AuditLog rows themselves (the webhook and callback paths) used to skip
// sanitizeDetails entirely, so a secret in Details was persisted verbatim.
func TestFlushAuditEntriesSanitizesDirectRows(t *testing.T) {
	s := mustTenantServer(t)

	rows := []db.AuditLog{
		{User: "system", Action: "probe", Details: "login with password=hunter2 succeeded", Success: true, CreatedAt: time.Now()},
		{User: "system", Action: "probe", Details: `{"api_key":"sk-live-abcdef"}`, Success: true, CreatedAt: time.Now()},
		{User: "system", Action: "probe", Details: "benign detail", Success: true, CreatedAt: time.Now()},
		{User: "system", Action: "probe", Error: "dial failed: token=abc123", Success: false, CreatedAt: time.Now()},
	}
	s.flushAuditEntriesSync(rows)

	var stored []db.AuditLog
	if err := s.db.Where("action = ?", "probe").Order("id asc").Find(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if len(stored) != len(rows) {
		t.Fatalf("stored %d rows, want %d", len(stored), len(rows))
	}
	for _, row := range stored {
		combined := row.Details + " " + row.Error
		for _, secret := range []string{"hunter2", "sk-live-abcdef", "abc123"} {
			if strings.Contains(combined, secret) {
				t.Fatalf("secret %q was persisted in an audit row: %+v", secret, row)
			}
		}
	}
	// A benign detail is left alone.
	if stored[2].Details != "benign detail" {
		t.Fatalf("a benign detail was rewritten: %q", stored[2].Details)
	}
}

// TestWebhookAuditDetailsHideTheToken pins the audit row itself: it outlives the
// log line and is shown in the console, and a Slack-style webhook URL carries no
// "key=" for sanitizeDetails to match, so the redaction has to happen here.
func TestWebhookAuditDetailsHideTheToken(t *testing.T) {
	const secretURL = "https://hooks.example.com/services/T000/B000/SUPERSECRET"
	got := webhookAuditDetails("alerts", secretURL, http.StatusOK)

	if strings.Contains(got, "SUPERSECRET") {
		t.Fatalf("the token is in the audit detail: %s", got)
	}
	if !strings.Contains(got, "hooks.example.com") {
		t.Fatalf("the host was dropped: %s", got)
	}
	if !strings.Contains(got, "200") || !strings.Contains(got, "alerts") {
		t.Fatalf("the detail lost the status or the name: %s", got)
	}
}
