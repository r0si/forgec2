package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/db"
)

// fakeSSEProvider serves one canned SSE body per request, cycling through the
// provided bodies, and records how many requests it saw.
func fakeSSEProvider(t *testing.T, bodies ...string) (*httptest.Server, *int32) {
	t.Helper()
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		idx := int(n) - 1
		if idx >= len(bodies) {
			idx = len(bodies) - 1
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(bodies[idx]))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func sseData(payload string) string {
	return "data: " + payload + "\n\n"
}

func collectSSE(t *testing.T, ch <-chan sseEvent) []sseEvent {
	t.Helper()
	var events []sseEvent
	deadline := time.After(10 * time.Second)
	for {
		select {
		case evt, ok := <-ch:
			if !ok {
				return events
			}
			events = append(events, evt)
		case <-deadline:
			t.Fatalf("converse did not finish; collected %d events", len(events))
		}
	}
}

func converseOpts(endpoint string) aiConversationOptions {
	return aiConversationOptions{provider: "openai", endpoint: endpoint, apiKey: "sk-test"}
}

func converseCtx() *aiReqCtx {
	return &aiReqCtx{Principal: aiPrincipal{UserID: 1, Username: "alice", TenantID: 1, Role: db.RoleAdmin}}
}

func TestConverseStreamsTextAndDone(t *testing.T) {
	s := assistTestServer(t)
	srv, calls := fakeSSEProvider(t,
		sseData(`{"choices":[{"delta":{"content":"Hello"}}]}`)+
			sseData(`{"choices":[{"delta":{"content":" world"},"finish_reason":"stop"}]}`)+
			sseData("[DONE]"))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	events := collectSSE(t, s.converse("test-model", "system prompt",
		[]chatMessage{{Role: "user", Content: "hi"}}, ctx, converseCtx(),
		converseOpts(srv.URL+"/v1/chat/completions")))

	if atomic.LoadInt32(calls) != 1 {
		t.Fatalf("provider calls = %d, want 1", atomic.LoadInt32(calls))
	}
	var lastText string
	sawDone, sawProgress := false, false
	for _, e := range events {
		switch e.Type {
		case "error":
			t.Fatalf("unexpected error event: %s", e.Data)
		case "text":
			lastText = e.Data
		case "done":
			sawDone = true
		case "progress":
			sawProgress = true
		}
	}
	if !sawProgress {
		t.Fatal("no progress event")
	}
	if !sawDone {
		t.Fatal("no done event")
	}
	if lastText != "Hello world" {
		t.Fatalf("final text = %q, want the accumulated answer", lastText)
	}
}

func TestConverseExecutesToolRound(t *testing.T) {
	s := assistTestServer(t)
	// Round 1: the model asks for list_agents. Round 2: it answers.
	srv, calls := fakeSSEProvider(t,
		sseData(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"list_agents","arguments":"{}"}}]}}]}`)+
			sseData(`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`)+
			sseData("[DONE]"),
		sseData(`{"choices":[{"delta":{"content":"Two agents are online."},"finish_reason":"stop"}]}`)+
			sseData("[DONE]"))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	events := collectSSE(t, s.converse("test-model", "you are helpful",
		[]chatMessage{{Role: "user", Content: "how many agents?"}}, ctx, converseCtx(),
		converseOpts(srv.URL+"/v1/chat/completions")))

	if n := atomic.LoadInt32(calls); n < 2 {
		t.Fatalf("provider calls = %d, want at least 2 (tool round + answer)", n)
	}
	sawTool, sawDone := false, false
	var lastText string
	for _, e := range events {
		switch e.Type {
		case "error":
			t.Fatalf("unexpected error event: %s", e.Data)
		case "tool":
			sawTool = true
			if !strings.Contains(e.Data, `"name":"list_agents"`) {
				t.Fatalf("tool event = %s", e.Data)
			}
			if strings.Contains(e.Data, `"error":"unknown tool"`) {
				t.Fatalf("tool dispatch failed: %s", e.Data)
			}
		case "text":
			lastText = e.Data
		case "done":
			sawDone = true
		}
	}
	if !sawTool || !sawDone {
		t.Fatalf("tool=%v done=%v", sawTool, sawDone)
	}
	if lastText != "Two agents are online." {
		t.Fatalf("final text = %q", lastText)
	}
}

func TestConverseReportsProviderErrors(t *testing.T) {
	s := assistTestServer(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"upstream boom"}}`))
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	events := collectSSE(t, s.converse("test-model", "sys",
		[]chatMessage{{Role: "user", Content: "hi"}}, ctx, converseCtx(),
		converseOpts(srv.URL+"/v1/chat/completions")))
	var sawError bool
	for _, e := range events {
		if e.Type == "error" && strings.Contains(e.Data, "AI request failed") {
			sawError = true
		}
	}
	if !sawError {
		t.Fatalf("no error event in %v", events)
	}
}

func TestConverseStopsOnCancelledContext(t *testing.T) {
	s := assistTestServer(t)
	srv, _ := fakeSSEProvider(t, sseData(`{"choices":[{"delta":{"content":"x"}}]}`))
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancelled before the loop starts
	events := collectSSE(t, s.converse("m", "s", []chatMessage{{Role: "user", Content: "hi"}}, ctx,
		converseCtx(), converseOpts(srv.URL+"/v1/chat/completions")))
	for _, e := range events {
		if e.Type == "error" {
			t.Fatalf("cancelled run emitted an error: %s", e.Data)
		}
	}
}

func TestParseStreamChunksHandlesBadPayload(t *testing.T) {
	s := assistTestServer(t)
	// An unparseable frame is skipped (providers emit keep-alives and
	// comments); [DONE] still terminates the stream cleanly.
	srv, _ := fakeSSEProvider(t, "data: not-json\n\ndata: [DONE]\n\n")
	resp, err := http.Get(srv.URL + "/v1/chat/completions")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	ch := make(chan sseEvent, 16)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	toolCalls, content, reasoning, _, _, err := s.parseStreamChunks(resp, ch, ctx)
	if err != nil {
		t.Fatalf("tolerant parse returned %v", err)
	}
	if content != "" || reasoning != "" || len(toolCalls) != 0 {
		t.Fatalf("skipped frame produced content: %q / %q / %d", content, reasoning, len(toolCalls))
	}
}
