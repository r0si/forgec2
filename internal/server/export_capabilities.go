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
		// The diagnostics surface: endpoints that answer "what would happen" and execute nothing. They were added over
		// several rounds and were only discoverable by reading the spec, which is the same problem the export half of
		// this document exists to solve.
		"diagnostics": []gin.H{
			{
				"path":         "/api/automation/rules/:id/preview",
				"method":       "POST",
				"answers":      "why would THIS rule fire, or why did it not",
				"body":         []string{"event_type", "data"},
				"side_effects": false,
				"notes":        []string{"executes nothing; audited as automation_preview"},
			},
			{
				"path":         "/api/automation/preview",
				"method":       "POST",
				"answers":      "which rules of this tenant would fire for one event",
				"body":         []string{"event_type", "data", "agent_*", "user"},
				"side_effects": false,
				"notes": []string{
					"rules for other event types and other tenants are omitted rather than listed as not firing",
					"executes nothing; audited as automation_preview_batch",
				},
			},
			{
				"path":         "/api/automation/health",
				"method":       "GET",
				"answers":      "which rules are unconditional, dangling or duplicated",
				"side_effects": false,
				"notes": []string{
					"read-only, no event needed",
					"target lookups include tenant-0 rows, so legacy resources are not reported missing",
				},
			},
			{
				"path":         "/api/automation/diagnose",
				"method":       "POST",
				"answers":      "what fires for this event AND who would be told",
				"body":         []string{"event_type", "notification", "data", "agent_*", "user"},
				"side_effects": false,
				"notes":        []string{"composes the two previews; executes nothing; audited as automation_diagnose"},
			},
			{
				"path":         "/api/notification-routes/preview",
				"method":       "POST",
				"answers":      "would this notification reach anyone, and which route rejects it",
				"body":         []string{"title", "severity"},
				"side_effects": false,
				"notes":        []string{"persists and delivers nothing; only successful previews are audited"},
			},
			{
				"path":         "/api/stats/tasks/failures",
				"method":       "GET",
				"answers":      "why tasks failed, grouped by reason, type or agent",
				"body":         []string{},
				"side_effects": false,
				"notes": []string{
					"reason codes are first-match and unmatched errors are collected as 'other' with an example",
					"a 5000-row sample reports truncated: true rather than implying a complete picture",
				},
			},
			{
				"path":         "/api/stats/failures/signatures",
				"method":       "GET",
				"answers":      "what SHAPE of failure dominates, across tasks and notifications",
				"body":         []string{},
				"side_effects": false,
				"notes": []string{
					"one row per normalised shape (paths, ids and digits collapsed), so four 401s read as one problem",
					"task signatures come from the window; notification signatures come from counters since process start",
				},
			}, {
				// The one entry in this list that DOES something outside: it sends a real message. A client that offers a
				// "run all diagnostics" button must not include it, which is why `side_effects` is stated on every entry
				// rather than implied by the absence of a note.
				"path":         "/api/notification-routes/:id/test",
				"method":       "POST",
				"answers":      "does this route actually deliver, right now",
				"body":         []string{},
				"side_effects": true,
				"notes": []string{
					"DELIVERS A REAL MESSAGE to the configured target; not a dry run",
					"rate-limited per route (10s, Retry-After on 429); a disabled route is 409",
					"the reason for a failure is in the response, not only the status",
				},
			},
			{
				// The second entry with side effects: it runs the rule's actions. A client offering "run all diagnostics" must
				// exclude this one too, which is why the flag is per entry rather than a property of the family.
				"path":         "/api/automation/rules/:id/test",
				"method":       "POST",
				"answers":      "do this rule's actions actually work, right now",
				"body":         []string{"confirm"},
				"side_effects": true,
				"notes": []string{
					"EXECUTES the rule's actions against a synthetic event; confirm: true is mandatory",
					"rate-limited per rule (10s, Retry-After on 429); every executed test is audited",
					"webhook delivery is asynchronous: its result arrives after the response and is counted on the route",
				},
			},
		},
		// The clearing endpoints are deliberately NOT in the list above: every diagnostic answers a question, while these
		// change what the report will say next. Keeping them apart means a client can offer "run all diagnostics" and a
		// separate, deliberate "clear the counters" action.
		"maintenance": []gin.H{
			{
				"path":    "/api/notification-routes/stats/reset",
				"method":  "POST",
				"clears":  "notification delivery counters",
				"audited": "notification_stats_reset",
				"params":  []string{"id (optional)"},
			},
			{
				"path":    "/api/automation/action-stats/reset",
				"method":  "POST",
				"clears":  "automation rule action counters",
				"audited": "automation_action_stats_reset",
				"params":  []string{"id (optional)"},
			},
		}, "diagnostic_rate_limit": gin.H{
			"requests":       diagnosticRateLimitRequests,
			"window_seconds": int(diagnosticRateLimitWindow.Seconds()),
			"scope":          "per client IP, on the diagnostics endpoints only",
			"notes": []string{
				"the diagnostics family is throttled separately from the write endpoints it shares a permission with",
				"a 429 carries Retry-After",
			},
		},
		// A filtered export looks exactly like an unfiltered one, so both downloads name the filters that shaped them. Published
		// here because a client should not have to reverse-engineer a header name from a changelog.
		"exposure": gin.H{
			"task_export_scope_header":  taskExportScopeHeader,
			"audit_export_scope_header": auditExportScopeHeader,
			"scope_format": "name=value;name=value (sorted by name), or \"all\" when nothing was filtered - " +
				"so a file carries its own scope and a filtered sample cannot be read as the whole picture",
			"task_export_truncated_header":  "X-Task-Export-Truncated",
			"audit_export_truncated_header": "X-Audit-Export-Truncated",
			"audit_export_filtered_header":  "X-Audit-Export-Filtered",
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
