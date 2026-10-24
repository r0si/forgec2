package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/plugin"
	"github.com/gin-gonic/gin"
)

func (s *Server) resolvePluginRecord(id string) (*db.Plugin, error) {
	var p db.Plugin
	if err := s.db.First(&p, id).Error; err == nil {
		return &p, nil
	}
	if err := s.db.Where("name = ?", id).First(&p).Error; err == nil {
		return &p, nil
	}
	return nil, fmt.Errorf("plugin not found")
}

func (s *Server) tryRegisterPluginFromDisk(name string) {
	if name == "" || s.pluginManager == nil {
		return
	}
	manifestPath := filepath.Join(s.pluginManager.PluginDir(name), "manifest.yaml")
	manifest, err := plugin.LoadManifest(manifestPath)
	if err != nil {
		return
	}
	if err := s.pluginManager.Register(manifest); err != nil {
		slog.Error("Failed to register plugin", "name", name, "error", err)
	}
}

func (s *Server) handlePluginExecuteInfo(c *gin.Context) {
	p, err := s.resolvePluginRecord(c.Param("id"))
	if err != nil {
		respondErrorSafe(c, http.StatusNotFound, err, "")
		return
	}
	runtime, err := s.pluginManager.Get(p.Name)
	if err != nil {
		respondErrorSafe(c, http.StatusNotFound, err, "")
		return
	}

	manifest, ok := pluginManifest(runtime)
	if !ok {
		respondError(c, http.StatusInternalServerError, "plugin has no manifest")
		return
	}

	respond(c, gin.H{
		"plugin": gin.H{
			"id":          p.ID,
			"name":        manifest.Name,
			"version":     manifest.Version,
			"type":        manifest.Type,
			"description": manifest.Description,
			"author":      manifest.Author,
			"category":    manifest.Category,
			"params":      manifest.Params,
			"events":      manifest.Events,
			"config":      manifest.Config,
		},
	})
}

func (s *Server) handlePluginExecute(c *gin.Context) {
	p, err := s.resolvePluginRecord(c.Param("id"))
	if err != nil {
		respondErrorSafe(c, http.StatusNotFound, err, "")
		return
	}
	var req struct {
		AgentID string                 `json:"agent_id"`
		Params  map[string]interface{} `json:"params"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "invalid request body")
		return
	}

	result, err := s.pluginManager.ExecuteCommand(c.Request.Context(), p.Name, req.AgentID, req.Params)
	if err != nil {
		respondErrorSafe(c, http.StatusBadRequest, err, "")
		return
	}
	respond(c, gin.H{"result": result})
}

func (s *Server) handlePluginReport(c *gin.Context) {
	p, err := s.resolvePluginRecord(c.Param("id"))
	if err != nil {
		respondErrorSafe(c, http.StatusNotFound, err, "")
		return
	}
	var req struct {
		Params map[string]interface{} `json:"params"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "invalid request body")
		return
	}

	report, err := s.pluginManager.GenerateReport(c.Request.Context(), p.Name, req.Params)
	if err != nil {
		respondErrorSafe(c, http.StatusBadRequest, err, "")
		return
	}
	respond(c, gin.H{
		"report": gin.H{
			"title":   report.Title,
			"format":  report.Format,
			"content": string(report.Content),
		},
	})
}

// pluginInstallStaging is a write-ahead record for one install request, so a
// rejected plugin never leaves its package behind on disk.
//
// A rejected manifest used to leave manifest.yaml and the uploaded script in
// the plugin directory after Register() failed, and tryRegisterPluginFromDisk
// would later pick them up — a package the trust layer had explicitly refused
// could still be activated. cleanup() rolls the filesystem back: files this
// request created are removed, and files this request overwrote are restored to
// their previous bytes, so reinstalling an existing plugin can never destroy it.
type pluginInstallStaging struct {
	pluginDir string
	entries   []stagedFile

	dirCreated bool
}

// stagedFile is one path this request wrote, with the bytes that were there
// before (when it already existed).
type stagedFile struct {
	path    string
	prior   []byte
	existed bool
}

// writeFile records and writes a file, remembering what was there before.
func (st *pluginInstallStaging) writeFile(path string, data []byte, perm os.FileMode) error {
	var prior []byte
	if existing, err := os.ReadFile(path); err == nil {
		prior = existing
	}
	entry := stagedFile{path: path}
	if prior != nil {
		entry.existed = true
		entry.prior = prior
	}
	if err := os.WriteFile(path, data, perm); err != nil {
		return err
	}
	st.entries = append(st.entries, entry)
	return nil
}

// cleanup rolls back every file this request wrote. The plugin directory is
// removed only when this request created it and it is empty afterwards, so a
// rejected reinstall cannot delete the directory an existing plugin lives in.
func (st *pluginInstallStaging) cleanup() {
	for _, entry := range st.entries {
		if entry.existed {
			if err := os.WriteFile(entry.path, entry.prior, 0600); err != nil && !errors.Is(err, os.ErrNotExist) {
				slog.Warn("Plugin install: failed to restore overwritten file", "path", entry.path, "err", err)
			}
			continue
		}
		if err := os.Remove(entry.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			slog.Warn("Plugin install: failed to remove staged file", "path", entry.path, "err", err)
		}
	}
	if !st.dirCreated {
		return
	}
	entries, err := os.ReadDir(st.pluginDir)
	if err != nil || len(entries) > 0 {
		return
	}
	if err := os.Remove(st.pluginDir); err != nil && !errors.Is(err, os.ErrNotExist) {
		slog.Warn("Plugin install: failed to remove empty plugin dir", "path", st.pluginDir, "err", err)
	}
}

func (s *Server) handlePluginInstall(c *gin.Context) {
	id := c.Param("id")

	manifestFile, err := c.FormFile("manifest")
	if err != nil {
		manifestFile = nil
	}
	if manifestFile == nil {
		p, err := s.resolvePluginRecord(id)
		if err != nil {
			respondErrorSafe(c, http.StatusNotFound, err, "")
			return
		}
		s.tryRegisterPluginFromDisk(p.Name)
		if err := s.db.Model(&db.Plugin{}).Where("name = ?", p.Name).Update("enabled", true).Error; err != nil {
			slog.Error("Failed to enable plugin record", "plugin", p.Name, "err", err)
		}
		s.LogAuditRecord(c, "plugin_install", "plugin", p.Name, fmt.Sprintf("enabled plugin %s", p.Name), true, nil)
		respond(c, gin.H{"plugin": p.Name})
		return
	}
	if manifestFile.Size > MaxUploadSize {
		respondError(c, http.StatusBadRequest, fmt.Sprintf("manifest file too large (max %d bytes)", MaxUploadSize))
		return
	}

	mf, err := manifestFile.Open()
	if err != nil {
		respondError(c, http.StatusInternalServerError, "failed to open manifest")
		return
	}
	defer mf.Close()

	manifestData, err := io.ReadAll(mf)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "failed to read manifest")
		return
	}

	var manifest plugin.Manifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		respondErrorSafe(c, http.StatusBadRequest, err, "invalid manifest json")
		return
	}

	if strings.ContainsAny(manifest.Name, `/\`) || manifest.Name == "" {
		respondError(c, http.StatusBadRequest, "invalid plugin name: must not contain path separators")
		return
	}
	if strings.Contains(manifest.Name, "..") {
		respondError(c, http.StatusBadRequest, "invalid plugin name: must not contain '..'")
		return
	}
	if manifest.Entry != "" {
		if strings.ContainsAny(manifest.Entry, `/\`) || strings.Contains(manifest.Entry, "..") {
			respondError(c, http.StatusBadRequest, "invalid entry point: must be a file inside the plugin directory")
			return
		}
		if filepath.IsAbs(manifest.Entry) {
			respondError(c, http.StatusBadRequest, "invalid entry point: must not be an absolute path")
			return
		}
	}

	pluginDir := s.pluginManager.PluginDir(manifest.Name)
	_, statErr := os.Stat(pluginDir)
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		respondError(c, http.StatusInternalServerError, "failed to inspect plugin directory")
		return
	}
	stage := &pluginInstallStaging{pluginDir: pluginDir, dirCreated: statErr != nil}
	// fail leaves whatever this request wrote on disk; the caller already sent
	// the response, so this only cleans up.
	fail := func(code int, msg string) {
		stage.cleanup()
		respondError(c, code, msg)
	}
	failSafe := func(code int, err error, fallback string) {
		stage.cleanup()
		respondErrorSafe(c, code, err, fallback)
	}

	if err := os.MkdirAll(pluginDir, 0750); err != nil {
		respondError(c, http.StatusInternalServerError, "failed to create plugin directory")
		return
	}

	manifestPath := filepath.Join(pluginDir, "manifest.yaml")
	if err := stage.writeFile(manifestPath, manifestData, 0600); err != nil {
		respondError(c, http.StatusInternalServerError, "failed to write manifest")
		return
	}

	scriptFile, err := c.FormFile("script")
	if err == nil {
		if scriptFile.Size > MaxUploadSize {
			respondError(c, http.StatusBadRequest, fmt.Sprintf("script file too large (max %d bytes)", MaxUploadSize))
			return
		}
		sf, err := scriptFile.Open()
		if err != nil {
			respondError(c, http.StatusInternalServerError, "failed to open script")
			return
		}
		defer sf.Close()
		scriptData, err := io.ReadAll(sf)
		if err != nil {
			respondError(c, http.StatusInternalServerError, "failed to read script")
			return
		}
		scriptPath := filepath.Join(pluginDir, manifest.Entry)
		if err := stage.writeFile(scriptPath, scriptData, 0600); err != nil {
			fail(http.StatusInternalServerError, "failed to write script")
			return
		}
	}

	// Register rejects bad signatures, unresolvable dependencies and duplicate
	// names. A rejection must not leave the package on disk.
	if err := s.pluginManager.Register(&manifest); err != nil {
		failSafe(http.StatusBadRequest, err, "")
		return
	}

	if err := s.db.Model(&db.Plugin{}).Where("name = ?", manifest.Name).Updates(map[string]interface{}{
		"enabled": true,
		"version": manifest.Version,
	}).Error; err != nil {
		slog.Error("Failed to update plugin record after install", "plugin", manifest.Name, "err", err)
	}

	s.LogAuditRecord(c, "plugin_install", "plugin", manifest.Name, fmt.Sprintf("installed plugin %s %s", manifest.Name, manifest.Version), true, nil)
	respond(c, gin.H{"plugin": manifest.Name})
}

func (s *Server) handlePluginEnable(c *gin.Context) {
	p, err := s.resolvePluginRecord(c.Param("id"))
	if err != nil {
		respondErrorSafe(c, http.StatusNotFound, err, "")
		return
	}
	if err := s.pluginManager.SetEnabled(p.Name, true); err != nil {
		respondErrorSafe(c, http.StatusBadRequest, err, "")
		return
	}
	if err := s.db.Model(p).Update("enabled", true).Error; err != nil {
		slog.Error("Failed to persist plugin enable", "plugin", p.Name, "err", err)
	}
	s.LogAuditRecord(c, "plugin_enable", "plugin", p.Name, "enabled plugin", true, nil)
	respond(c, gin.H{})
}

func (s *Server) handlePluginDisable(c *gin.Context) {
	p, err := s.resolvePluginRecord(c.Param("id"))
	if err != nil {
		respondErrorSafe(c, http.StatusNotFound, err, "")
		return
	}
	if err := s.pluginManager.SetEnabled(p.Name, false); err != nil {
		respondErrorSafe(c, http.StatusBadRequest, err, "")
		return
	}
	if err := s.db.Model(p).Update("enabled", false).Error; err != nil {
		slog.Error("Failed to persist plugin disable", "plugin", p.Name, "err", err)
	}
	s.LogAuditRecord(c, "plugin_disable", "plugin", p.Name, "disabled plugin", true, nil)
	respond(c, gin.H{})
}

// pluginManifest extracts the manifest from an external plugin wrapper.
func pluginManifest(p plugin.Plugin) (*plugin.Manifest, bool) {
	type manifestHolder interface {
		Manifest() *plugin.Manifest
	}
	if mh, ok := p.(manifestHolder); ok {
		return mh.Manifest(), true
	}
	return nil, false
}
