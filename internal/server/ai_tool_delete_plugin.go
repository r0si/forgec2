package server

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/forgec2/forgec2/internal/db"
)

// Deleting an assistant-authored plugin.
//
// The lifecycle needs an end: create_plugin writes one, run_plugin checks it, update_plugin iterates -
// and without delete the assistant's experiments accumulate in the console forever. The same ownership
// guard applies as for update: a model removes its own output, never a package a human wrote, because
// the installed set is the operator's inventory and a model cannot see what depends on it.
//
// `plugins.delete` is its own permission (the console already separates it from write and execute), so
// "may run plugins" and "may remove them" can be granted independently.
func (s *Server) aiDeletePluginTool(reqCtx *aiReqCtx, argsJSON string) string {
	if s.pluginManager == nil {
		return `{"error":"the plugin manager is not available on this server"}`
	}
	var args struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return `{"error":"invalid arguments: ` + jsonSafeError(err, "plugin") + `"}`
	}
	name := strings.ToLower(strings.TrimSpace(args.Name))
	if name == "" {
		return `{"error":"name is required"}`
	}

	var record db.Plugin
	if err := s.db.Where("name = ?", name).First(&record).Error; err != nil {
		names := s.pluginNamesForAI()
		return fmt.Sprintf(`{"error":"no plugin named %q; registered: %s"}`, name, strings.Join(names, ", "))
	}
	if record.Author != aiPluginAuthor {
		return fmt.Sprintf(`{"error":"plugin %q was not written by the assistant (author %q); remove it from the Plugins page instead"}`, name, record.Author)
	}

	// Unregister unloads the plugin, drops the record and removes the package directory - the same three
	// steps the delete handler performs, so there is one definition of "deleted" in the product.
	version := record.Version
	if err := s.pluginManager.Unregister(name); err != nil {
		return fmt.Sprintf(`{"error":"failed to delete the plugin: %s"}`, jsonSafeError(err, "plugin"))
	}

	user := "ai"
	if reqCtx != nil && reqCtx.Principal.Username != "" {
		user = reqCtx.Principal.Username
	}
	s.LogAuditRecord(nil, "plugin_delete_ai", "plugin", name,
		fmt.Sprintf("assistant deleted plugin %s (%s) on behalf of %s", name, version, user), true, nil)

	out, ok := marshalJSONSafe(map[string]interface{}{
		"deleted": true,
		"name":    name,
		"version": version,
	})
	if !ok {
		return `{"error":"the plugin was deleted but the result could not be marshalled"}`
	}
	return string(out)
}
