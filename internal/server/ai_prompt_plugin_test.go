package server

import (
	"strings"
	"testing"
)

// TestDefaultPromptTeachesPluginAuthoring keeps the assistant aware of the plugin workflow.
//
// The four plugin tools exist only if the model knows to use them, and the default prompt is the one
// place that knowledge lives when an operator has not written their own. Dropping these lines (or the
// ABI note) silently removes the capability: the tools stay callable, but the model stops chaining them
// and stops knowing that plain text on stdout is a parse error. This test is the reminder that the
// prompt is part of the feature.
func TestDefaultPromptTeachesPluginAuthoring(t *testing.T) {
	prompt := defaultAISystemPrompt

	required := []struct {
		needle string
		why    string
	}{
		{"create_plugin", "the model must know it can author a package"},
		{"run_plugin", "authoring without checking is how a broken plugin ships"},
		{"update_plugin", "iteration is the point of the loop"},
		{"delete_plugin", "experiments must be cleanable"},
		{`{"success":true,"output":"..."}`, "the JSON stdout ABI, otherwise every generated plugin fails to parse"},
		{"stdin", "the input side of the ABI"},
		{"Only packages the assistant authored", "otherwise the model may believe it can rewrite operator code"},
	}
	for _, item := range required {
		if !strings.Contains(prompt, item.needle) {
			t.Errorf("the default prompt no longer mentions %q (%s)", item.needle, item.why)
		}
	}

	// The workflow order matters: author, then verify, then clean up.
	author := strings.Index(prompt, "create_plugin")
	verify := strings.Index(prompt, "run_plugin")
	if author < 0 || verify < 0 || author > verify {
		t.Errorf("the prompt does not present create before run (create=%d run=%d)", author, verify)
	}
}
