package server

import (
	"encoding/json"
	"fmt"
	"html"
	"log/slog"
	"net/smtp"
	"strings"
	"time"

	"github.com/forgec2/forgec2/internal/db"
)

const emailMaxRetries = 3

type EmailConfig struct {
	SMTPHost string `json:"smtp_host"`
	SMTPPort int    `json:"smtp_port"`
	SMTPUser string `json:"smtp_user"`
	SMTPPass string `json:"smtp_pass"`
	From     string `json:"from"`
	To       string `json:"to"`
}

func (e EmailConfig) addr() string {
	return fmt.Sprintf("%s:%d", e.SMTPHost, e.SMTPPort)
}

func (e EmailConfig) auth() smtp.Auth {
	if e.SMTPUser == "" {
		return nil
	}
	return smtp.PlainAuth("", e.SMTPUser, e.SMTPPass, e.SMTPHost)
}

func (s *Server) triggerEmailNotifications(evt Event) {
	var cfgStr db.ServerConfig
	if err := s.db.Where("key = ?", "notification_targets").First(&cfgStr).Error; err != nil {
		return
	}
	var targets struct {
		Email EmailConfig `json:"email"`
	}
	if err := json.Unmarshal([]byte(cfgStr.Value), &targets); err != nil {
		slog.Error("Failed to parse email config", "error", err)
		return
	}
	cfg := targets.Email
	if cfg.SMTPHost == "" || cfg.To == "" {
		return
	}

	body := formatEmailBody(evt)
	msg := buildMIMEMessage(cfg.From, cfg.To, fmt.Sprintf("ForgeC2 Alert: %s", evt.Type), body)

	if err := sendEmailWithRetry(cfg, msg); err != nil {
		slog.Error("Email notification failed after retries", "event", evt.Type, "agent", evt.AgentID, "error", err)
		s.flushAuditEntries([]db.AuditLog{{
			User:    "system",
			Action:  "email_notification",
			Success: false,
			Details: fmt.Sprintf("Email notification failed for event %s on agent %s: %v", evt.Type, evt.AgentID, err),
		}})
	} else {
		s.flushAuditEntries([]db.AuditLog{{
			User:    "system",
			Action:  "email_notification",
			Success: true,
			Details: fmt.Sprintf("Email notification sent for event %s on agent %s", evt.Type, evt.AgentID),
		}})
	}
}

func sendEmailWithRetry(cfg EmailConfig, msg []byte) error {
	backoff := []time.Duration{time.Second, 3 * time.Second, 7 * time.Second}
	for attempt := 0; attempt <= emailMaxRetries; attempt++ {
		if err := smtp.SendMail(cfg.addr(), cfg.auth(), cfg.From, []string{cfg.To}, msg); err != nil {
			if attempt < emailMaxRetries {
				slog.Warn("Email delivery failed, retrying", "to", cfg.To, "attempt", attempt+1, "error", err)
				time.Sleep(backoff[attempt])
				continue
			}
			return fmt.Errorf("email delivery failed after %d retries: %w", emailMaxRetries, err)
		}
		return nil
	}
	return fmt.Errorf("email delivery failed")
}

// sanitizeHeaderValue removes CR/LF so a value cannot end the header block or add one.
// The Subject is built from the event type, and the addresses come from configuration, so
// this is defence in depth rather than the main fix - but a header that can be split is
// never worth leaving open.
func sanitizeHeaderValue(v string) string {
	if i := strings.IndexAny(v, "\r\n"); i >= 0 {
		// Cut at the first line break instead of folding it: the tail is exactly what an
		// attacker wanted appended (a Bcc, a Content-Type), and nothing legitimate puts a
		// newline in a Subject or an address.
		return strings.TrimSpace(v[:i])
	}
	return v
}

func buildMIMEMessage(from, to, subject, body string) []byte {
	headers := map[string]string{
		"From":         sanitizeHeaderValue(from),
		"To":           sanitizeHeaderValue(to),
		"Subject":      sanitizeHeaderValue(subject),
		"MIME-Version": "1.0",
		"Content-Type": "text/html; charset=\"UTF-8\"",
	}
	var buf strings.Builder
	for k, v := range headers {
		buf.WriteString(fmt.Sprintf("%s: %s\r\n", k, v))
	}
	buf.WriteString("\r\n")
	buf.WriteString(body)
	return []byte(buf.String())
}

func formatEmailBody(evt Event) string {
	// Every value below is interpolated into HTML. Hostname, IP and OS are reported by the
	// IMPLANT, so an attacker who controls a beacon controls what the operator's mail client
	// renders: unescaped, that is an HTML injection into a trusted alert mail (a plausible
	// "click here to re-authenticate" link, or a tracking pixel that fires when the message
	// is opened). Escape at the boundary.
	esc := html.EscapeString
	agentInfo := ""
	if evt.AgentID != "" {
		agentInfo = fmt.Sprintf(`
<tr><td style="padding:8px;border-bottom:1px solid #ddd;color:#555;">Agent ID</td><td style="padding:8px;border-bottom:1px solid #ddd;font-weight:bold;">%s</td></tr>
<tr><td style="padding:8px;border-bottom:1px solid #ddd;color:#555;">Hostname</td><td style="padding:8px;border-bottom:1px solid #ddd;font-weight:bold;">%s</td></tr>
<tr><td style="padding:8px;border-bottom:1px solid #ddd;color:#555;">IP</td><td style="padding:8px;border-bottom:1px solid #ddd;color:#555;">%s</td></tr>
<tr><td style="padding:8px;border-bottom:1px solid #ddd;color:#555;">OS</td><td style="padding:8px;border-bottom:1px solid #ddd;font-weight:bold;">%s</td></tr>
`, esc(evt.AgentID), esc(evt.AgentHost), esc(evt.AgentIP), esc(evt.AgentOS))
	}
	dataStr := ""
	if len(evt.Data) > 0 {
		d, _ := json.MarshalIndent(evt.Data, "", "  ")
		dataStr = fmt.Sprintf("<pre style=\"background:#f5f5f5;padding:10px;border-radius:4px;font-size:12px;\">%s</pre>", esc(string(d)))
	}
	return fmt.Sprintf(`<!DOCTYPE html>
<html><head><meta charset="UTF-8"></head><body style="font-family:Arial,sans-serif;background:#f9f9f9;padding:20px;">
<div style="max-width:600px;margin:0 auto;background:#fff;border-radius:8px;box-shadow:0 2px 4px rgba(0,0,0,0.1);">
<div style="background:#1a1a2e;color:#fff;padding:20px;border-radius:8px 8px 0 0;">
<h2 style="margin:0;font-size:18px;">ForgeC2 Alert</h2>
<p style="margin:5px 0 0;font-size:14px;opacity:0.8;">%s</p>
</div>
<div style="padding:20px;">
<p style="color:#555;margin-top:0;">Event <strong>%s</strong> triggered at <strong>%s</strong>.</p>
<table style="width:100%%;border-collapse:collapse;margin-top:15px;">%s</table>
%s
</div>
<div style="padding:15px 20px;background:#f5f5f5;border-radius:0 0 8px 8px;font-size:12px;color:#999;text-align:center;">
ForgeC2 Security Platform
</div>
</div></body></html>`, esc(string(evt.Type)), esc(string(evt.Type)), esc(evt.Timestamp.Format(time.RFC1123)), agentInfo, dataStr)
}
