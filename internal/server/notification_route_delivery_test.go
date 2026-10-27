package server

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/db"
)

// TestSendNotificationRouteRevalidatesAtDelivery pins the legacy-row guard: the
// delivery path re-checks the target because rows created before validation
// existed (or before DNS changed) are still in the table. Without it, a stored
// private target would be dialled on every notification.
func TestSendNotificationRouteRevalidatesAtDelivery(t *testing.T) {
	s := mustTenantServer(t)
	s.ctx = context.Background()
	s.httpClient = &http.Client{Timeout: 300 * time.Millisecond}

	n := &db.Notification{ID: 1, Type: "test", Title: "t", Message: "m", Severity: "info"}

	for _, target := range []string{
		"http://127.0.0.1:8000/hook",
		"http://169.254.169.254/latest/meta-data/",
		"http://10.0.0.5/hook",
		"http://[::1]/hook",
	} {
		route := db.NotificationRoute{Name: "legacy", Channel: "webhook", Target: target, Enabled: true}
		err := s.sendNotificationRoute(route, n)
		if err == nil {
			t.Fatalf("a private target was dialled: %s", target)
		}
		if !strings.Contains(err.Error(), "rejected") {
			t.Fatalf("target %s failed for the wrong reason: %v", target, err)
		}
	}

	// An unknown channel is refused before any validation chatter.
	if err := s.sendNotificationRoute(db.NotificationRoute{Name: "x", Channel: "carrier-pigeon", Target: "https://example.com"}, n); err == nil {
		t.Fatal("an unknown channel was accepted")
	}
}

// TestRedactNotificationRouteKeepsTelegramChatID pins the asymmetry: a Telegram
// target is a chat id (useful to the operator, not a secret) while Discord and
// generic webhook targets carry their token in the URL and must be masked.
func TestRedactNotificationRouteKeepsTelegramChatID(t *testing.T) {
	telegram := redactNotificationRoute(db.NotificationRoute{Channel: "telegram", Target: "-100123456", Secret: "bot-token"})
	if telegram.Target != "-100123456" {
		t.Fatalf("a telegram chat id was masked: %q", telegram.Target)
	}
	if telegram.Secret == "bot-token" || telegram.Secret == "" {
		t.Fatalf("the bot token was not masked: %q", telegram.Secret)
	}

	for _, channel := range []string{"discord", "slack", "webhook", "dingtalk", "wecom", "feishu"} {
		route := redactNotificationRoute(db.NotificationRoute{
			Channel: channel, Target: "https://hooks.example.com/services/T/B/SECRET", Secret: "s3cr3t",
		})
		if strings.Contains(route.Target, "SECRET") {
			t.Fatalf("%s target leaked through the list endpoint: %q", channel, route.Target)
		}
		if route.Secret == "s3cr3t" {
			t.Fatalf("%s secret leaked through the list endpoint", channel)
		}
	}

	// Empty values stay empty (the UI shows "not set", not a mask).
	empty := redactNotificationRoute(db.NotificationRoute{Channel: "webhook"})
	if empty.Target != "" || empty.Secret != "" {
		t.Fatalf("empty fields were turned into masks: %+v", empty)
	}
}

// TestValidateNotificationRouteRejectsMaskedRoundTrip pins the update path: a UI
// that posts back the mask instead of a new value must not overwrite the stored
// credential with the literal mask.
func TestValidateNotificationRouteRejectsMaskedRoundTrip(t *testing.T) {
	if err := validateNotificationRoute("telegram", notificationRouteMask, notificationRouteMask); err == nil {
		t.Fatal("a masked telegram round-trip was accepted as a real token")
	}
	if err := validateNotificationRoute("discord", notificationRouteMask, ""); err == nil {
		t.Fatal("a masked webhook target was accepted")
	}
	if err := validateNotificationRoute("nope", "https://example.com", ""); err == nil {
		t.Fatal("an unknown channel was accepted")
	}
	if err := validateNotificationRoute("webhook", "http://127.0.0.1/x", ""); err == nil {
		t.Fatal("a private target was accepted at save time")
	}
	_ = time.Now()
}
