package mcp

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestMCPProtocolAndTools covers the handshake, the tool list and one call end to end.
//
// The point of the read-only design is that these tools cannot see more than the operator who started them, so the test drives a
// fake console that answers only when the cookie is presented - and checks that a rejection reaches the model as a tool error
// rather than as a broken transport.
func TestMCPProtocolAndTools(t *testing.T) {
	console := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") != "forgec2_session=abc" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/agents":
			_, _ = w.Write([]byte(`{"success":true,"data":{"agents":[{"id":"a-1","hostname":"web-01","status":"online","os":"windows","username":"svc","last_seen":"2026-10-29T10:00:00Z"}],"total":1}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer console.Close()

	var out bytes.Buffer
	server := NewServer("forgec2", "1", &out)
	if err := RegisterReadOnlyTools(server, NewClient(console.URL, "forgec2_session=abc")); err != nil {
		t.Fatalf("register: %v", err)
	}

	// 1. initialize answers with the pinned revision and the tool capability.
	out.Reset()
	server.handleLine([]byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}` + "\n"))
	var init struct {
		Result struct {
			ProtocolVersion string `json:"protocolVersion"`
			ServerInfo      struct {
				Name string `json:"name"`
			} `json:"serverInfo"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out.Bytes(), &init); err != nil {
		t.Fatalf("initialize: %v (%s)", err, out.String())
	}
	if init.Result.ProtocolVersion != ProtocolVersion || init.Result.ServerInfo.Name != "forgec2" {
		t.Fatalf("initialize = %+v", init.Result)
	}

	// 2. tools/list advertises the read-only set and nothing that mutates.
	out.Reset()
	server.handleLine([]byte(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}` + "\n"))
	var list struct {
		Result struct {
			Tools []Tool `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out.Bytes(), &list); err != nil {
		t.Fatalf("tools/list: %v (%s)", err, out.String())
	}
	names := map[string]bool{}
	for _, tool := range list.Result.Tools {
		names[tool.Name] = true
		if tool.Description == "" || len(tool.InputSchema) == 0 {
			t.Fatalf("tool %q is advertised without a description or schema", tool.Name)
		}
	}
	for _, want := range []string{"list_sessions", "failure_signatures", "queue_depth", "session_timeline"} {
		if !names[want] {
			t.Fatalf("tool %q is not advertised (got %v)", want, names)
		}
	}

	// 3. A call goes through the console and comes back as text plus structured content.
	out.Reset()
	server.handleLine([]byte(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"list_sessions","arguments":{"online_only":true}}}` + "\n"))
	var call struct {
		Result struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out.Bytes(), &call); err != nil {
		t.Fatalf("tools/call: %v (%s)", err, out.String())
	}
	if call.Result.IsError {
		t.Fatalf("the call failed: %s", out.String())
	}
	if len(call.Result.Content) != 1 || !strings.Contains(call.Result.Content[0].Text, "web-01") {
		t.Fatalf("the summary does not name the session: %+v", call.Result.Content)
	}

	// 4. A refused cookie is a TOOL error, not a protocol error: the model must see the reason.
	var rejected bytes.Buffer
	unauthenticated := NewServer("forgec2", "1", &rejected)
	if err := RegisterReadOnlyTools(unauthenticated, NewClient(console.URL, "forgec2_session=wrong")); err != nil {
		t.Fatalf("register: %v", err)
	}
	unauthenticated.handleLine([]byte(`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"list_sessions","arguments":{}}}` + "\n"))
	var refused struct {
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
		Error interface{} `json:"error"`
	}
	if err := json.Unmarshal(rejected.Bytes(), &refused); err != nil {
		t.Fatalf("refused call: %v (%s)", err, rejected.String())
	}
	if refused.Error != nil {
		t.Fatalf("a rejected cookie became a protocol error: %s", rejected.String())
	}
	if !refused.Result.IsError || !strings.Contains(refused.Result.Content[0].Text, "refused") {
		t.Fatalf("the refusal was not explained to the caller: %s", rejected.String())
	}

	// 5. An unknown tool and a bad method are protocol errors, as the spec says.
	out.Reset()
	server.handleLine([]byte(`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"nope","arguments":{}}}` + "\n"))
	if !strings.Contains(out.String(), "unknown tool") {
		t.Fatalf("unknown tool = %s", out.String())
	}
	out.Reset()
	server.handleLine([]byte(`{"jsonrpc":"2.0","id":6,"method":"resources/list"}` + "\n"))
	if !strings.Contains(out.String(), "unknown method") {
		t.Fatalf("unknown method = %s", out.String())
	}
}
