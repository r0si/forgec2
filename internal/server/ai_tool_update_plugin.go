package server

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/plugin"
	"gopkg.in/yaml.v3"
)

// Updating an AI-authored plugin.
//
// create_plugin + run_plugin close the authoring loop, but iterating on a plugin meant deleting and
// recreating it - which loses the plugin's id, its reviews, its config and any operator edits to the
// manifest. `update_plugin` rewrites the entry file (and bumps the patch version) in place.
//
// One guard makes this safe to expose: the tool only touches a package **the assistant authored**
// (`Author == aiPluginAuthor`). A hand-written or imported plugin is never overwritten by a model,
// because a model cannot see the intent behind code it did not write - the operator edits those.
const aiPluginAuthor = "forgec2-ai"

// aiUpdatePluginArgs is the model-facing argument set.
type aiUpdatePluginArgs struct {
	Name        string `json:"name"`
	Code        string `json:"code"`
	Description string `json:"description"`
	Version     string `json:"version"`
}

// aiUpdatePluginTool rewrites an assistant-authored plugin's entry file and re-registers it.
func (s *Server) aiUpdatePluginTool(reqCtx *aiReqCtx, argsJSON string) string {
	if s.pluginManager == nil {
		return `{"error":"the plugin manager is not available on this server"}`
	}
	var args aiUpdatePluginArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return `{"error":"invalid arguments: ` + jsonSafeError(err, "plugin") + `"}`
	}
	name := strings.ToLower(strings.TrimSpace(args.Name))
	if name == "" {
		return `{"error":"name is required"}`
	}
	code := args.Code
	if strings.TrimSpace(code) == "" {
		return `{"error":"code is required"}`
	}
	if len(code) > aiPluginCodeMaxBytes {
		return fmt.Sprintf(`{"error":"code is too large: %d bytes, limit %d"}`, len(code), aiPluginCodeMaxBytes)
	}

	var record db.Plugin
	if err := s.db.Where("name = ?", name).First(&record).Error; err != nil {
		return fmt.Sprintf(`{"error":"no plugin named %q"}`, name)
	}
	// The ownership guard: a model may iterate on its own output, never on someone else's code.
	if record.Author != aiPluginAuthor {
		return fmt.Sprintf(`{"error":"plugin %q was not written by the assistant (author %q); edit it from the Plugins page instead"}`, name, record.Author)
	}

	dir := s.pluginManager.PluginDir(name)
	manifestPath := filepath.Join(dir, "manifest.yaml")
	rawManifest, err := os.ReadFile(manifestPath)
	if err != nil {
		return fmt.Sprintf(`{"error":"the plugin package has no readable manifest: %s"}`, jsonSafeError(err, "plugin"))
	}
	var manifest plugin.Manifest
	if err := yaml.Unmarshal(rawManifest, &manifest); err != nil {
		return fmt.Sprintf(`{"error":"the existing manifest is invalid: %s"}`, jsonSafeError(err, "plugin"))
	}
	previous := manifest
	if strings.TrimSpace(args.Description) != "" {
		manifest.Description = strings.TrimSpace(args.Description)
	}
	// A patch bump unless the caller says otherwise: version is what the console shows, so a silent
	// in-place change would leave "1.0.0" describing two different programs.
	if v := strings.TrimSpace(args.Version); v != "" {
		manifest.Version = v
	} else {
		manifest.Version = bumpPatchVersion(manifest.Version)
	}

	entryPath := filepath.Join(dir, filepath.Base(manifest.Entry))
	entryName := filepath.Base(manifest.Entry)
	previousEntry, readErr := os.ReadFile(entryPath)
	hadEntry := readErr == nil

	// Unregister is what unloads the old code, but it ALSO deletes the package directory and the plugin
	// record - so the new files are written AFTER it, from the backup in memory. Writing first and
	// unregistering second left Register loading a manifest whose files had just been removed, which is
	// exactly the "plugin failed" the first test run produced.
	if err := s.pluginManager.Unregister(name); err != nil {
		return fmt.Sprintf(`{"error":"failed to unload the previous version: %s"}`, jsonSafeError(err, "plugin"))
	}
	restorePrevious := func() {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return
		}
		if hadEntry {
			_ = os.WriteFile(entryPath, previousEntry, 0o600)
		}
		_ = os.WriteFile(manifestPath, rawManifest, 0o600)
		if rerr := s.pluginManager.Register(&previous); rerr != nil {
			slog.Error("AI plugin update: could not restore the previous plugin", "plugin", name, "err", rerr)
		}
	}

	if err := os.MkdirAll(dir, 0o750); err != nil {
		restorePrevious()
		return `{"error":"failed to recreate the plugin directory"}`
	}
	// Entry first, then the manifest: if the manifest write fails, the backup restore is unambiguous.
	if err := os.WriteFile(filepath.Join(dir, entryName), []byte(code), 0o600); err != nil {
		restorePrevious()
		return `{"error":"failed to write the new code"}`
	}
	newManifest, err := yaml.Marshal(&manifest)
	if err != nil {
		restorePrevious()
		return `{"error":"failed to render the updated manifest"}`
	}
	if err := os.WriteFile(manifestPath, newManifest, 0o600); err != nil {
		restorePrevious()
		return `{"error":"failed to write the updated manifest"}`
	}
	if err := s.pluginManager.Register(&manifest); err != nil {
		restorePrevious()
		return fmt.Sprintf(`{"error":"the updated package was rejected and the previous version was restored: %s"}`, jsonSafeError(err, "plugin"))
	}

	// Unregister also dropped the record, so it is recreated rather than updated.
	if err := s.db.Where(&db.Plugin{Name: name}).Assign(db.Plugin{
		Version:     manifest.Version,
		Description: manifest.Description,
		Author:      manifest.Author,
		Type:        manifest.Type,
		Category:    manifest.Category,
		Enabled:     true,
	}).FirstOrCreate(&db.Plugin{Name: name}).Error; err != nil {
		slog.Error("AI plugin update: failed to restore the plugin record", "plugin", name, "err", err)
	}

	user := "ai"
	if reqCtx != nil && reqCtx.Principal.Username != "" {
		user = reqCtx.Principal.Username
	}
	s.LogAuditRecord(nil, "plugin_update_ai", "plugin", name,
		fmt.Sprintf("assistant updated plugin %s to %s on behalf of %s", name, manifest.Version, user), true, nil)

	out, ok := marshalJSONSafe(map[string]interface{}{
		"updated": true,
		"name":    name,
		"version": manifest.Version,
		"entry":   manifest.Entry,
		"note":    "re-registered but NOT executed; use run_plugin to check the new behaviour",
	})
	if !ok {
		return `{"error":"failed to marshal the result"}`
	}
	return string(out)
}

// bumpPatchVersion increments the last numeric component of a version string, leaving anything
// unparseable untouched rather than inventing a version.
func bumpPatchVersion(version string) string {
	parts := strings.Split(strings.TrimSpace(version), ".")
	if len(parts) == 0 {
		return version
	}
	last := parts[len(parts)-1]
	n := 0
	valid := len(last) > 0
	for _, r := range last {
		if r < '0' || r > '9' {
			valid = false
			break
		}
		n = n*10 + int(r-'0')
	}
	if !valid {
		return version
	}
	parts[len(parts)-1] = fmt.Sprintf("%d", n+1)
	return strings.Join(parts, ".")
}
