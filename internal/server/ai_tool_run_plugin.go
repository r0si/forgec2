package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/forgec2/forgec2/internal/plugin"
)

// Running a plugin from the assistant closes the authoring loop: after `create_plugin` writes a package
// it is worth knowing whether it actually works, and making the operator leave the conversation to find
// out is how a plugin ships broken.
//
// The tool is not a shortcut around the existing gate. It requires `plugins.execute` (a separate
// permission from `plugins.write`, so authoring and running can be granted separately), it goes through
// the approval intent like every other write-risk tool, and it executes through the same
// `Manager.ExecuteCommand` the Plugins page uses - inheriting its concurrency quota, per-plugin
// timeout and output caps rather than inventing new ones.

const (
	// aiPluginRunTimeout bounds one run from the assistant. The plugin's own timeout is usually
	// shorter; this is the outer bound so a wedged child cannot hold the tool call.
	aiPluginRunTimeout = 90 * time.Second
	// aiPluginRunOutputBytes is how much of the result is shown to the model. A plugin that prints a
	// megabyte has not answered the question, and the full output stays available on the page.
	aiPluginRunOutputBytes = 8 * 1024
)

// aiRunPluginArgs is the model-facing argument set.
type aiRunPluginArgs struct {
	Name    string                 `json:"name"`
	AgentID string                 `json:"agent_id"`
	Params  map[string]interface{} `json:"params"`
}

// aiRunPluginTool executes a registered command plugin and returns its result.
func (s *Server) aiRunPluginTool(reqCtx *aiReqCtx, argsJSON string) string {
	if s.pluginManager == nil {
		return `{"error":"the plugin manager is not available on this server"}`
	}
	var args aiRunPluginArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return `{"error":"invalid arguments: ` + jsonSafeError(err, "plugin") + `"}`
	}
	name := strings.TrimSpace(args.Name)
	if name == "" {
		return `{"error":"name is required (list registered plugins first if unsure)"}`
	}

	p, err := s.pluginManager.Get(name)
	if err != nil {
		names := s.pluginNamesForAI()
		return fmt.Sprintf(`{"error":"no such plugin; registered: %s"}`, strings.Join(names, ", "))
	}
	if !p.IsEnabled() {
		return fmt.Sprintf(`{"error":"plugin %q is disabled; enable it on the Plugins page first"}`, name)
	}
	if p.Type() != "command" {
		return fmt.Sprintf(`{"error":"plugin %q is a %s plugin; only command plugins can be run directly"}`, name, p.Type())
	}

	params := args.Params
	if params == nil {
		params = map[string]interface{}{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), aiPluginRunTimeout)
	defer cancel()

	started := time.Now()
	result, runErr := s.pluginManager.ExecuteCommand(ctx, name, strings.TrimSpace(args.AgentID), params)
	elapsed := time.Since(started).Milliseconds()

	if runErr != nil {
		// The plugin never produced a Result: a timeout, a missing interpreter, a refused run.
		out, ok := marshalJSONSafe(map[string]interface{}{
			"ran":         false,
			"plugin":      name,
			"error":       jsonSafeError(runErr, "plugin"),
			"duration_ms": elapsed,
			"note":        "the plugin did not produce output; check the server log and the plugin's interpreter",
		})
		if !ok {
			return `{"error":"the plugin failed and the failure could not be marshalled"}`
		}
		return string(out)
	}

	payload := map[string]interface{}{
		"ran":         true,
		"plugin":      name,
		"success":     result.Success,
		"duration_ms": elapsed,
	}
	if result.Output != "" {
		output := result.Output
		if len(output) > aiPluginRunOutputBytes {
			// Say it was truncated: a silently clipped result reads as a complete one.
			output = output[:aiPluginRunOutputBytes] + "\n[output truncated; open the Plugins page for the full text]"
			payload["truncated"] = true
		}
		payload["output"] = output
	}
	if result.Error != "" {
		payload["error"] = truncateString(result.Error, 1000)
	}
	if result.Data != nil {
		payload["data"] = result.Data
	}
	out, ok := marshalJSONSafe(payload)
	if !ok {
		return `{"error":"the plugin produced a result that could not be marshalled"}`
	}

	// Audit the run like an operator-triggered execution would be; a plugin is code with the server's
	// privileges, so "who ran it" belongs in the trail.
	user := "ai"
	if reqCtx != nil && reqCtx.Principal.Username != "" {
		user = reqCtx.Principal.Username
	}
	s.LogAuditRecord(nil, "plugin_execute_ai", "plugin", name,
		fmt.Sprintf("assistant ran plugin %s on behalf of %s (success=%v)", name, user, result.Success), true, nil)

	return string(out)
}

// pluginNamesForAI lists registered plugins so a wrong name can be corrected in one turn.
func (s *Server) pluginNamesForAI() []string {
	plugins := s.pluginManager.List()
	names := make([]string, 0, len(plugins))
	for _, p := range plugins {
		names = append(names, p.Name())
	}
	if len(names) == 0 {
		return []string{"(none registered yet - use create_plugin first)"}
	}
	return names
}

// jsonSafeError renders an error for a JSON string field without quoting problems.
func jsonSafeError(err error, fallback string) string {
	return strings.ReplaceAll(truncateString(sanitizeError(err, fallback), 300), `"`, "'")
}

var _ = plugin.Manifest{} // keep the import meaningful if the tool is ever narrowed
