package server

import (
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
)

// Plugin load failures.
//
// A package that is on disk but cannot be registered is silently absent: it does not appear in the plugin list, it cannot be
// run, and the only trace was a `slog.Warn` at startup. That is the "reports success, nothing happened" shape this codebase
// keeps finding - an operator who installed a plugin and sees nothing assumes the console is stale, not that the manifest
// was rejected.
//
// The manager already records the latest reason per package (`LoadFailures`); this exposes it, read-only.
//
// GET /api/plugins/failures

// handlePluginLoadFailures lists the packages discovered but rejected.
func (s *Server) handlePluginLoadFailures(c *gin.Context) {
	if s.pluginManager == nil {
		// No manager means no plugin directory was configured; that is not an error, and saying "0 failures" would be a
		// lie about a system that never looked.
		respond(c, gin.H{"failures": []gin.H{}, "count": 0, "plugins_enabled": false})
		return
	}

	failures := s.pluginManager.LoadFailures()
	names := make([]string, 0, len(failures))
	for name := range failures {
		names = append(names, name)
	}
	// Stable order: a diagnostic list that reshuffles on every poll is hard to compare with the last one.
	sort.Strings(names)

	out := make([]gin.H, 0, len(names))
	for _, name := range names {
		reason := failures[name]
		out = append(out, gin.H{
			"name":   name,
			"reason": reason,
			// The reason is the whole value of this endpoint - "failed to load" alone sends the operator to the logs.
			"hint": pluginFailureHint(reason),
		})
	}
	respond(c, gin.H{"failures": out, "count": len(out), "plugins_enabled": true})
}

// pluginFailureHint turns a rejection reason into the next thing to try.
//
// Deliberately short and matched on the same words the validator uses; anything unrecognised gets no invented advice.
func pluginFailureHint(reason string) string {
	switch {
	case strings.Contains(strings.ToLower(reason), "signature"), strings.Contains(strings.ToLower(reason), "digest"):
		return "the package was modified after signing: re-stamp it with cmd/sign-plugin, and check plugins.trusted_keys"
	case strings.Contains(strings.ToLower(reason), "manifest"), strings.Contains(strings.ToLower(reason), "yaml"):
		return "the manifest is invalid: run `go run ./cmd/plugin-check <dir>` and fix every error-level finding"
	case strings.Contains(strings.ToLower(reason), "entry"), strings.Contains(strings.ToLower(reason), "interpreter"):
		return "the entry file or interpreter is missing: check manifest.entry and the interpreter tier"
	case strings.Contains(strings.ToLower(reason), "untrusted"), strings.Contains(strings.ToLower(reason), "unknown signing key"):
		return "the signing key is not in plugins.trusted_keys: add the key, or re-sign with one that is"
	default:
		return ""
	}
}
