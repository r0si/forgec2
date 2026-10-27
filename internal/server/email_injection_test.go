package server

import (
	"strings"
	"testing"
	"time"
)

// TestAlertEmailEscapesImplantControlledFields pins a real injection: the alert mail rendered
// Hostname, IP, OS and the event payload straight into HTML, and those values are reported by
// the IMPLANT. An attacker who controls a beacon controls what the operator's mail client
// renders - a plausible "re-authenticate here" link inside a mail the operator trusts, or a
// tracking pixel that fires on open.
func TestAlertEmailEscapesImplantControlledFields(t *testing.T) {
	evt := Event{
		Type:      "agent_online",
		AgentID:   "a1",
		AgentHost: `<img src=x onerror="alert(1)">`,
		AgentIP:   `10.0.0.1"><script>alert(2)</script>`,
		AgentOS:   `<b>windows</b>`,
		Timestamp: time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC),
		Data:      map[string]interface{}{"note": `<script>alert(3)</script>`},
	}

	body := formatEmailBody(evt)

	for _, raw := range []string{"<img src=x", "<script>alert(2)", "<b>windows</b>", "<script>alert(3)"} {
		if strings.Contains(body, raw) {
			t.Fatalf("raw markup reached the mail body: %q", raw)
		}
	}
	for _, escaped := range []string{"&lt;img src=x", "&lt;script&gt;alert(2)", "&lt;b&gt;windows&lt;/b&gt;"} {
		if !strings.Contains(body, escaped) {
			t.Fatalf("expected escaped form %q in the body", escaped)
		}
	}

	// The legitimate content is still readable, so escaping did not gut the template.
	if !strings.Contains(body, "10.0.0.1") {
		t.Fatal("the agent IP disappeared from the mail body")
	}
	if !strings.Contains(body, "ForgeC2 Alert") {
		t.Fatal("the template lost its heading")
	}
}

// TestMIMEMessageCannotHaveHeadersSplit keeps a CR/LF out of the header block: a value that
// can end a header can add one (Bcc, Content-Type).
func TestMIMEMessageCannotHaveHeadersSplit(t *testing.T) {
	msg := string(buildMIMEMessage(
		"c2@example.test",
		"ops@example.test\r\nBcc: victim@example.test",
		"ForgeC2 Alert: x\r\nX-Injected: 1",
		"<p>body</p>",
	))

	headerBlock := msg[:strings.Index(msg, "\r\n\r\n")]
	if strings.Contains(headerBlock, "Bcc:") {
		t.Fatalf("a second To value added a header: %q", headerBlock)
	}
	if strings.Contains(headerBlock, "X-Injected:") {
		t.Fatalf("the subject added a header: %q", headerBlock)
	}
	if !strings.Contains(headerBlock, "Subject: ForgeC2 Alert: x") {
		t.Fatalf("the subject was mangled beyond recognition: %q", headerBlock)
	}
}
