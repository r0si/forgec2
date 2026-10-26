package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/config"
)

type webhookParams = struct {
	URL     string            `json:"url"`
	Method  string            `json:"method"`
	Headers map[string]string `json:"headers"`
	Secret  string            `json:"secret"`
}

// TestExecuteWebhookSignatureAndMethod covers the delivery contract: the default
// method is POST, the JSON body is the event, custom headers land on the
// request, and a configured secret produces an HMAC over the exact bytes sent.
func TestExecuteWebhookSignatureAndMethod(t *testing.T) {
	type captured struct {
		method  string
		headers http.Header
		body    []byte
		at      time.Time
	}
	got := make(chan captured, 2)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got <- captured{method: r.Method, headers: r.Header.Clone(), body: body, at: time.Now()}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	s := mustTenantServer(t)
	s.httpClient = &http.Client{Timeout: 5 * time.Second}
	// The delivery tests need a local receiver; the SSRF gate is exercised by
	// TestExecuteWebhookRefusesInternalTargets below with the real validator.
	webhookTargetValidator = func(string) error { return nil }
	t.Cleanup(func() { webhookTargetValidator = validateWebhookURL })

	evt := Event{Type: EventImplantCheckin, TenantID: 1, Data: map[string]interface{}{"agent_id": "a1"}}
	params := webhookParams{
		URL:     srv.URL,
		Headers: map[string]string{"X-Custom": "yes"},
		Secret:  "s3cret",
	}
	s.executeWebhook(params, evt)

	select {
	case c := <-got:
		if c.method != http.MethodPost {
			t.Fatalf("method = %q, want the POST default", c.method)
		}
		if c.headers.Get("X-Custom") != "yes" {
			t.Fatal("custom header missing")
		}
		if c.headers.Get("User-Agent") != "ForgeC2-Automation/1.0" {
			t.Fatalf("user agent = %q", c.headers.Get("User-Agent"))
		}
		// The signature is an HMAC over the body the receiver actually got.
		mac := hmac.New(sha256.New, []byte(params.Secret))
		mac.Write(c.body)
		want := hex.EncodeToString(mac.Sum(nil))
		if got := c.headers.Get("X-ForgeC2-Signature"); got != want {
			t.Fatalf("signature = %q, want %q", got, want)
		}
		var decoded Event
		if err := json.Unmarshal(c.body, &decoded); err != nil {
			t.Fatalf("body is not the event JSON: %v", err)
		}
		if decoded.Type != evt.Type {
			t.Fatalf("event type = %q", decoded.Type)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("webhook was not delivered")
	}

	// No secret: no signature header at all (an empty HMAC would be a lie).
	params.Secret = ""
	s.executeWebhook(params, evt)
	select {
	case c := <-got:
		if c.headers.Get("X-ForgeC2-Signature") != "" {
			t.Fatal("a signature was sent without a secret")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("second webhook was not delivered")
	}
}

// TestExecuteWebhookRefusesInternalTargets pins the SSRF guard: a webhook aimed
// at a private address must never leave the server, even though the target is
// reachable from it.
func TestExecuteWebhookRefusesInternalTargets(t *testing.T) {
	// The receiver must never be hit; fail the test if it is.
	hit := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit <- struct{}{}
	}))
	defer srv.Close()

	s := mustTenantServer(t)
	s.httpClient = &http.Client{Timeout: 2 * time.Second}
	s.cfg = &config.Config{}

	s.executeWebhook(webhookParams{URL: srv.URL}, Event{Type: EventImplantCheckin})

	select {
	case <-hit:
		t.Fatal("a webhook to a private address was delivered")
	case <-time.After(300 * time.Millisecond):
		// Expected: validateWebhookURL refused it before any request.
	}
}
