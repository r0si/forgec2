package server

import (
	"sort"
	"strings"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/plugin"
	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
	"net/http"
	"os"
	"path/filepath"
)

// Plugin index: the installed set as one machine-readable document.
//
// This is the foundation a package registry needs (the role Sliver's Armory plays): an operator can
// export the plugin set of a deployment, diff two installations, or hand the index to another team
// without copying a directory tree. Each entry carries the package DIGEST computed with the same
// function the trust policy uses (plugin.PackageDigest), so the index is verifiable rather than
// descriptive - a receiver can recompute it and compare.
//
// Deliberately read-only this round. Installing FROM an index needs a trust decision (whose index? over
// what transport?) that deserves its own design, and shipping the export first keeps the surface small.
//
// GET /api/plugins/index            -> { plugins: [...] }

// pluginIndexEntry is one plugin in the exported index.
type pluginIndexEntry struct {
	Name        string   `json:"name"`
	Version     string   `json:"version"`
	Type        string   `json:"type"`
	Description string   `json:"description,omitempty"`
	Author      string   `json:"author,omitempty"`
	Category    string   `json:"category,omitempty"`
	Interpreter string   `json:"interpreter,omitempty"`
	Entry       string   `json:"entry,omitempty"`
	Params      []string `json:"params,omitempty"`
	// Digest is the package digest ("sha256:..."), or empty when the package directory is missing -
	// reported rather than faked, so an index never claims a package it cannot see.
	Digest string `json:"digest,omitempty"`
	// Signed reports whether the manifest carries a signature; the key itself is not exported.
	Signed bool `json:"signed"`
	// Loaded distinguishes a plugin the manager has in memory from a record whose package is missing.
	Loaded bool `json:"loaded"`
}

// handlePluginIndex answers the installed set as an index document.
func (s *Server) handlePluginIndex(c *gin.Context) {
	var records []db.Plugin
	if err := s.db.WithContext(s.ctx).Order("name asc").Find(&records).Error; err != nil {
		handleQueryError(c, err, "Failed to list plugins for the index")
		return
	}

	entries := make([]pluginIndexEntry, 0, len(records))
	for _, record := range records {
		entry := pluginIndexEntry{
			Name:        record.Name,
			Version:     record.Version,
			Type:        record.Type,
			Description: record.Description,
			Author:      record.Author,
			Category:    record.Category,
		}
		if s.pluginManager != nil {
			if loaded, err := s.pluginManager.Get(record.Name); err == nil && loaded != nil {
				entry.Loaded = true
			}
			dir := s.pluginManager.PluginDir(record.Name)
			if manifest, err := plugin.LoadManifest(filepath.Join(dir, "manifest.yaml")); err == nil {
				entry.Interpreter = manifest.Interpreter
				entry.Entry = manifest.Entry
				entry.Signed = strings.TrimSpace(manifest.Signature) != ""
				for _, param := range manifest.Params {
					entry.Params = append(entry.Params, param.Name)
				}
				if digest, derr := plugin.PackageDigest(dir, manifest.Includes, dir); derr == nil {
					entry.Digest = digest
				} else if !os.IsNotExist(derr) {
					// A digest failure on a present package is worth knowing about, but must not fail
					// the whole index.
					entry.Digest = ""
				}
			}
		}
		entries = append(entries, entry)
	}

	// Deterministic order: an index that shuffles cannot be diffed between deployments.
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })

	respond(c, gin.H{
		"count":   len(entries),
		"plugins": entries,
	})
}

var (
	_ = binding.JSON
	_ = http.StatusOK
)
