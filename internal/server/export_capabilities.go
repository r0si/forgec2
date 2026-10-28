package server

import (
	"crypto/ed25519"
	"encoding/hex"

	"github.com/gin-gonic/gin"
)

// Export capabilities: the download surface described in a form a client can read.
//
// Both exports grew parameters over a series of rounds - time bounds, engagement, format, column selection - and the
// only way to discover them was the OpenAPI document or the source. A UI that wants to build the download form, or a
// script that wants to validate its own arguments, can ask instead.
//
// The values are DERIVED from the code that enforces them (`ExportTaskLimit`, `exportFields`, the audit byte cap)
// rather than written out here, and a test pins that: a capabilities document that drifts from the behaviour is
// worse than none, because it is trusted.
//
// GET /api/exports/capabilities

// exportCapabilities is one endpoint described.
type exportCapabilities struct {
	Path       string   `json:"path"`
	Formats    []string `json:"formats"`
	Parameters []string `json:"parameters"`
	// MaxRows is the row cap the handler enforces (0 = unbounded).
	MaxRows int `json:"max_rows,omitempty"`
	// MaxBytes is the byte cap a buffered export enforces (0 = none).
	MaxBytes int64 `json:"max_bytes,omitempty"`
	// Fields are the selectable columns, when the endpoint supports projection.
	Fields []string `json:"fields,omitempty"`
	// Presets maps a preset name to the columns it expands to (empty slice = all).
	Presets map[string][]string `json:"presets,omitempty"`
	// Headers are the response headers that carry meaning beyond the body.
	Headers []string `json:"headers,omitempty"`
	// SupportsRanges reports whether the response honours a Range header.
	SupportsRanges bool `json:"supports_ranges,omitempty"`
	// Notes carry the constraints a client cannot infer, such as why projection is absent.
	Notes []string `json:"notes,omitempty"`
}

// handleExportCapabilities answers what the download endpoints accept.
func (s *Server) handleExportCapabilities(c *gin.Context) {
	taskFields := make([]string, 0, len(exportFields))
	for _, field := range exportFields {
		taskFields = append(taskFields, field.name)
	}

	respond(c, gin.H{
		"task_export": exportCapabilities{
			Path:       "/api/tasks/export",
			Formats:    []string{"csv", "json"},
			Parameters: []string{"limit", "since", "until", "engagement", "format", "fields"},
			MaxRows:    ExportTaskLimit,
			Fields:     taskFields,
			Presets: map[string][]string{
				"minimal": exportMinimalFields,
				"default": nil,
				"full":    nil,
			},
			Headers: []string{"ETag", "Content-Disposition", "X-Task-Export-Truncated"},
			Notes: []string{
				"the default column set is format-aware: the CSV keeps its nine historical columns, JSON adds agent_id",
				"a preset must be the whole fields value; mixing one with column names is a 400",
			},
		},
		"audit_export": exportCapabilities{
			Path:       "/api/audit/export",
			Formats:    []string{"ndjson", "json"},
			Parameters: []string{"limit", "since_id", "since", "until", "user", "action", "agent_id", "format"},
			MaxRows:    200000,
			MaxBytes:   int64(auditExportMaxBytes),
			Headers: []string{
				"ETag", "Content-Disposition", "X-Audit-Export-Rows", "X-Audit-Export-Filtered",
				"X-Audit-Export-Truncated", "Warning",
			},
			Notes: []string{
				"no column projection: offline verification needs every field the entry hash covers, so a projected export would look verifiable while being unverifiable",
				"any time, user, action or agent filter makes the rows a selection rather than a contiguous chain; the response says so in X-Audit-Export-Filtered and a Warning header",
			},
		},
		"task_output_download": exportCapabilities{
			Path:           "/api/tasks/:taskId/output",
			Formats:        []string{"text/plain"},
			Parameters:     []string{"taskId"},
			Headers:        []string{"X-Task-Status", "X-Task-Final", "Accept-Ranges", "Content-Range"},
			SupportsRanges: true,
			Notes: []string{
				"a failed task still downloads, with the error appended to the output",
				"nothing to download is a 404 rather than a zero-byte file",
			},
		},
		"plugins_index": exportCapabilities{
			Path:       "/api/plugins/index",
			Formats:    []string{"json"},
			Parameters: []string{},
			Notes: []string{
				"each entry carries the package digest from plugin.PackageDigest, so a receiver can recompute it",
				"POST /api/plugins/index/verify compares an uploaded index against the installation",
			},
		},
		"engagement_bundle": gin.H{
			"path":        "/api/engagements/:id/bundle",
			"formats":     []string{"application/zip"},
			"parameters":  []string{"id", "include"},
			"members":     bundleMemberNames,
			"verify_path": "/api/engagements/:id/bundle/verify",
			// Whether a bundle will be signed is an operational fact an operator should not have to discover by
			// producing one: without a key every handover is unsigned, and nothing in the console said so.
			"signed":              bundleSigner() != nil,
			"signature_algorithm": bundleSignatureAlgorithm(),
			"public_key":          bundlePublicKeyHex(),
			"headers": []string{
				"X-Bundle-SHA256", "X-Bundle-Members", "X-Bundle-Signed",
				"X-Bundle-Signature-Algorithm", "X-Bundle-Public-Key", "Content-Disposition",
			},
			"notes": []string{
				"MANIFEST.json always travels and is not selectable: without it the archive cannot be verified",
				"a bundle over the size cap is refused with 413 rather than truncated",
				"signing needs FORGEC2_BUNDLE_SIGNING_KEY (hex 32-byte Ed25519 seed); unsigned is honest, not silent",
			},
		},
		"limits": gin.H{
			"task_export_rows":   ExportTaskLimit,
			"audit_export_rows":  200000,
			"audit_export_bytes": int64(auditExportMaxBytes),
			"stats_sample_rows":  taskDurationSampleLimit,
		},
	})
}

// bundleSignatureAlgorithm names the algorithm a signed bundle uses, empty when the deployment cannot sign.
func bundleSignatureAlgorithm() string {
	if bundleSigner() == nil {
		return ""
	}
	return "ed25519"
}

// bundlePublicKeyHex reports the key a receiver should compare against, empty when unsigned.
//
// Publishing it here is not a trust decision: it lets an operator hand the expected key to whoever receives the bundle,
// which is exactly what the verify endpoint needs to turn "internally consistent" into "trusted".
func bundlePublicKeyHex() string {
	key := bundleSigner()
	if key == nil {
		return ""
	}
	return hex.EncodeToString(key.Public().(ed25519.PublicKey))
}
