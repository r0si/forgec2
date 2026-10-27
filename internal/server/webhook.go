package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"gorm.io/gorm"
)

const webhookMaxRetries = 3

func (s *Server) triggerWebhooks(evt Event) {
	for _, wh := range s.webhooksForEvent(evt) {
		go func() { _ = s.fireWebhook(wh, evt) }() // fireWebhook logs its own delivery failures
	}
}

// webhooksForEvent loads enabled webhooks for the event type, contained to
// the event's tenant: a webhook must never receive another tenant's event
// data (payloads carry hostnames, IPs and finding details). Legacy
// agent-less events (tenant 0) fire only legacy webhooks. Split out for
// testability (fireWebhook itself does network I/O).
func (s *Server) webhooksForEvent(evt Event) []db.WebhookConfig {
	var webhooks []db.WebhookConfig
	q := s.db.Where("event_type = ? AND enabled = ?", string(evt.Type), true)
	if evt.TenantID != 0 {
		q = q.Where("tenant_id = ?", evt.TenantID)
	} else {
		q = q.Where("tenant_id = ?", 0)
	}
	if err := q.Limit(200).Find(&webhooks).Error; err != nil {
		slog.Error("Failed to query webhooks", "err", err)
	}
	return webhooks
}

// redactURLFromMessage removes a target URL from a log message. Outbound targets are
// frequently secrets in themselves (a Slack incoming-webhook URL, or a provider URL
// with ?key=... appended), and a *url.Error stringifies the whole URL - so logging the raw
// delivery error writes the token into the log, right below a line that
// deliberately avoids logging the URL.
func redactURLFromMessage(msg, rawURL string) string {
	if rawURL == "" || msg == "" {
		return msg
	}
	return strings.ReplaceAll(msg, rawURL, redactURLForLog(rawURL))
}

// redactURLForLog keeps the scheme and host (enough to tell endpoints apart) and
// drops the path and query, which is where the token lives.
func redactURLForLog(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return "<webhook-url>"
	}
	return u.Scheme + "://" + u.Host + "/<redacted>"
}

// webhookBackoffSteps is the delay before each retry. webhookMaxRetries must not
// exceed its length; webhookRetryDelay reuses the last step if it ever does, so a
// raised constant cannot index out of range at runtime.
var webhookBackoffSteps = []time.Duration{time.Second, 3 * time.Second, 7 * time.Second}

// webhookRetryDelay returns the delay before the next attempt, reusing the last
// step if webhookMaxRetries is ever raised past the length of the table.
func webhookRetryDelay(attempt int) time.Duration {
	if attempt < 0 {
		return webhookBackoffSteps[0]
	}
	if attempt >= len(webhookBackoffSteps) {
		return webhookBackoffSteps[len(webhookBackoffSteps)-1]
	}
	return webhookBackoffSteps[attempt]
}

// webhookAuditDetails renders the delivery audit row. The URL is redacted: an
// audit row outlives the log line and is shown in the console, and a webhook URL
// is a credential. sanitizeDetails does not catch it - a Slack-style URL carries
// no "key=" for the pattern to match.
func webhookAuditDetails(name, rawURL string, status int) string {
	return fmt.Sprintf("Webhook %s -> %s: %d", name, redactURLForLog(rawURL), status)
}

func (s *Server) fireWebhook(wh db.WebhookConfig, evt Event) error {
	if err := validateWebhookURL(wh.URL); err != nil {
		// No URL in the log: webhook targets may embed secret tokens.
		slog.Error("Webhook URL rejected", "name", wh.Name, "error", err)
		return fmt.Errorf("webhook URL rejected: %w", err)
	}
	payload, err := json.Marshal(map[string]interface{}{
		"event":     evt.Type,
		"agent_id":  evt.AgentID,
		"hostname":  evt.AgentHost,
		"timestamp": evt.Timestamp,
		"data":      evt.Data,
	})
	if err != nil {
		slog.Error("Webhook marshal payload failed", "error", err)
		return fmt.Errorf("webhook payload marshal failed: %w", err)
	}

	for attempt := 0; attempt <= webhookMaxRetries; attempt++ {
		select {
		case <-s.ctx.Done():
			return fmt.Errorf("webhook delivery aborted: server shutting down")
		default:
		}

		req, err := http.NewRequestWithContext(s.ctx, wh.Method, wh.URL, bytes.NewReader(payload))
		if err != nil {
			return fmt.Errorf("webhook request build failed: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "ForgeC2-Webhook/1.0")

		if wh.Headers != "" {
			var hdr map[string]string
			if json.Unmarshal([]byte(wh.Headers), &hdr) == nil {
				for k, v := range hdr {
					req.Header.Set(k, v)
				}
			}
		}

		resp, err := ssrfSafeClient(s.httpClient).Do(req)
		if err != nil {
			if attempt < webhookMaxRetries {
				slog.Warn("Webhook delivery failed, retrying", "name", wh.Name, "attempt", attempt+1, "error", redactURLFromMessage(err.Error(), wh.URL))
				select {
				case <-s.ctx.Done():
					return fmt.Errorf("webhook delivery aborted: server shutting down")
				case <-time.After(webhookRetryDelay(attempt)):
				}
				continue
			}
			slog.Error("Webhook delivery failed, exhausted retries", "name", wh.Name, "error", redactURLFromMessage(err.Error(), wh.URL))
			return err
		}
		resp.Body.Close()

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			s.flushAuditEntries([]db.AuditLog{{
				User:    "system",
				Action:  "webhook",
				Success: true,
				Details: webhookAuditDetails(wh.Name, wh.URL, resp.StatusCode),
			}})
			// Record the real delivery so the integrations list can show
			// truthful event_count/last_trigger instead of fabricated zeroes.
			now := time.Now()
			if err := s.db.Model(&db.WebhookConfig{}).
				Where("id = ?", wh.ID).
				Updates(map[string]interface{}{
					"event_count":  gorm.Expr("event_count + 1"),
					"last_trigger": now,
				}).Error; err != nil {
				slog.Error("Failed to update webhook delivery stats", "name", wh.Name, "error", err)
			}
			return nil
		}
		if attempt < webhookMaxRetries && resp.StatusCode >= 500 {
			slog.Warn("Webhook delivery got server error, retrying", "name", wh.Name, "status", resp.StatusCode, "attempt", attempt+1)
			select {
			case <-s.ctx.Done():
				return fmt.Errorf("webhook delivery aborted: server shutting down")
			case <-time.After(webhookRetryDelay(attempt)):
			}
			continue
		}

		slog.Error("Webhook delivery failed with non-retryable status", "name", wh.Name, "status", resp.StatusCode)
		s.flushAuditEntries([]db.AuditLog{{
			User:    "system",
			Action:  "webhook",
			Success: false,
			Details: webhookAuditDetails(wh.Name, wh.URL, resp.StatusCode),
		}})
		return fmt.Errorf("webhook delivery failed with status %d", resp.StatusCode)
	}
	return fmt.Errorf("webhook delivery failed")
}
