package server

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestBuildToolEvent pins the SSE "tool" frame. The frame used to be built with
// Sprintf, so a model-supplied tool name containing a quote, or a tool whose
// output is not JSON, produced an INVALID JSON frame that the console could not
// parse — the tool result silently disappeared from the transcript.
func TestBuildToolEvent(t *testing.T) {
	// A JSON object result stays an object.
	out := buildToolEvent("call-1", "list_agents", `{"count":2,"agents":[]}`)
	if !json.Valid([]byte(out)) {
		t.Fatalf("invalid frame: %s", out)
	}
	var env struct {
		ID     string          `json:"id"`
		Name   string          `json:"name"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatal(err)
	}
	if env.ID != "call-1" || env.Name != "list_agents" {
		t.Fatalf("frame = %+v", env)
	}
	var inner map[string]any
	if err := json.Unmarshal(env.Result, &inner); err != nil {
		t.Fatalf("result is not an object: %v (%s)", err, env.Result)
	}
	if inner["count"].(float64) != 2 {
		t.Fatalf("result payload = %v", inner)
	}

	// A plain-text result (the compactor falls back to raw text for non-JSON
	// tool output) must be carried as a JSON string, not spliced in.
	text := "line one\nline \"two\"</script>"
	out = buildToolEvent("call-2", "run_macro", text)
	if !json.Valid([]byte(out)) {
		t.Fatalf("invalid frame for a text result: %s", out)
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatal(err)
	}
	var asString string
	if err := json.Unmarshal(env.Result, &asString); err != nil {
		t.Fatalf("text result is not a string: %v (%s)", err, env.Result)
	}
	if asString != text {
		t.Fatalf("text round-trip = %q, want %q", asString, text)
	}

	// A tool name containing quotes must not break the frame.
	evil := `ran "rm -rf" && echo 'x'`
	out = buildToolEvent("call-3", evil, `{}`)
	if !json.Valid([]byte(out)) {
		t.Fatalf("invalid frame for a quoted tool name: %s", out)
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatal(err)
	}
	if env.Name != evil {
		t.Fatalf("name round-trip = %q", env.Name)
	}

	// An empty result is carried as an empty string, not as a bare %s.
	out = buildToolEvent("call-4", "noop", "")
	if !json.Valid([]byte(out)) {
		t.Fatalf("invalid frame for an empty result: %s", out)
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(env.Result)) != `""` {
		t.Fatalf("empty result = %s, want a JSON string", env.Result)
	}
}

// TestCompactToolResultJSONNeverReturnsInvalidFrameInput documents why
// buildToolEvent has to defend itself: the compacted result is raw text whenever
// the tool output was not JSON.
func TestCompactToolResultJSONNeverReturnsInvalidFrameInput(t *testing.T) {
	plain := "just some text with a \" quote and\na newline"
	if got := compactToolResultJSON(plain); json.Valid([]byte(got)) {
		t.Fatalf("expected raw text for a non-JSON tool result, got valid JSON: %s", got)
	}
	// And buildToolEvent turns exactly that into a parseable frame.
	if out := buildToolEvent("id", "tool", compactToolResultJSON(plain)); !json.Valid([]byte(out)) {
		t.Fatalf("frame invalid: %s", out)
	}
}
