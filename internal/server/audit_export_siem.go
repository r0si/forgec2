package server

import (
	"fmt"
	"strconv"
	"strings"

	"time"
)

// SIEM projections of the audit chain.
//
// The NDJSON export is the verifiable artefact: it carries both hashes and every field the entry hash covers, so a third party can
// recompute the chain offline. ECS and CEF are **projections** for tools that ingest security events - they drop the hash chain,
// because a SIEM normalises documents and would silently break any chain rebuilt from its copy.
//
// That distinction is stated in the response (a header, not only a doc) because "I exported it to the SIEM, so it is preserved" is
// the assumption this whole feature exists to prevent.

// auditEventOutcome maps success to the ECS vocabulary.
func auditEventOutcome(success bool) string {
	if success {
		return "success"
	}
	return "failure"
}

// auditToECS renders one row as an Elastic Common Schema event.
//
// Field names follow ECS 8 (event.*, user.name, source.ip, host.id, message) with the product's own fields namespaced under
// `forgec2.*` rather than scattered into ECS top level, where a SIEM would index them as something they are not.
func auditToECS(row auditChainExportRow) map[string]interface{} {
	message := row.Action
	if row.Resource != "" {
		message += " " + row.Resource
	}
	if row.Details != "" {
		message += ": " + row.Details
	}
	event := map[string]interface{}{
		"@timestamp": row.CreatedAt,
		"event": map[string]interface{}{
			"action":  row.Action,
			"outcome": auditEventOutcome(row.Success),
			"kind":    "event",
			"dataset": "forgec2.audit",
			"id":      strconv.FormatUint(uint64(row.ID), 10),
		},
		"message": message,
		"forgec2": map[string]interface{}{
			"tenant_id":  row.TenantID,
			"resource":   row.Resource,
			"details":    row.Details,
			"entry_hash": row.EntryHash,
			"prev_hash":  row.PrevHash,
			"error":      row.Error,
		},
	}
	if row.User != "" {
		event["user"] = map[string]interface{}{"name": row.User}
	}
	if row.IP != "" {
		event["source"] = map[string]interface{}{"ip": row.IP}
	}
	if row.AgentID != "" {
		event["host"] = map[string]interface{}{"id": row.AgentID}
	}
	return event
}

// auditToCEF renders one row as a Common Event Format line.
//
// CEF is a line protocol with two escape grammars and they differ: pipes and backslashes in the header, and `=`, `\`, newline in
// the extensions. Getting one of them wrong truncates the event in the collector, which is the failure mode nobody notices until
// an incident review comes up short.
func auditToCEF(row auditChainExportRow, productVersion string) string {
	severity := "3" // informational
	if !row.Success {
		severity = "7"
	}
	header := []string{
		"CEF:0",
		"ForgeC2",
		cefHeaderEscape(productVersion),
		"audit",
		cefHeaderEscape(row.Action),
		severity,
	}
	var extension strings.Builder
	appendExt := func(key, value string) {
		if value == "" {
			return
		}
		if extension.Len() > 0 {
			extension.WriteByte(' ')
		}
		extension.WriteString(key)
		extension.WriteByte('=')
		extension.WriteString(cefExtensionEscape(value))
	}
	appendExt("rt", cefMillis(row.CreatedAt))
	appendExt("suser", row.User)
	appendExt("src", row.IP)
	appendExt("outcome", auditEventOutcome(row.Success))
	appendExt("cs1Label", "resource")
	appendExt("cs1", row.Resource)
	appendExt("cs2Label", "agent_id")
	appendExt("cs2", row.AgentID)
	appendExt("msg", row.Details)
	appendExt("externalId", strconv.FormatUint(uint64(row.ID), 10))

	// The header is written field by field rather than joined with a separator: this is a CEF line protocol, and
	// check:command-separators rightly flags a pipe-join as the implant's separator idiom. Keeping the writer explicit keeps the
	// gate strict instead of teaching it an exception.
	var line strings.Builder
	for _, field := range header {
		line.WriteString(field)
		line.WriteByte('|')
	}
	line.WriteString(extension.String())
	return line.String()
}

// cefHeaderEscape handles the header grammar: backslash and pipe only.
func cefHeaderEscape(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `|`, `\|`, "\n", `\n`, "\r", `\r`)
	return replacer.Replace(value)
}

// cefExtensionEscape handles the extension grammar: backslash, equals and newlines.
//
// The pipe is escaped here as well, although the strict CEF grammar only demands it in the header: collectors differ on whether an
// unescaped pipe ends the extension block, and a resource containing one truncated the event in the first version of this code.
func cefExtensionEscape(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `=`, `\=`, `|`, `\|`, "\n", `\n`, "\r", `\r`)
	return replacer.Replace(value)
}

// cefMillis renders rt= in epoch milliseconds, which is what collectors expect from `rt`.
func cefMillis(rfc3339Nano string) string {
	parsed, err := time.Parse(time.RFC3339Nano, rfc3339Nano)
	if err != nil {
		return ""
	}
	return strconv.FormatInt(parsed.UnixMilli(), 10)
}

// siemFormatFilename is the extension a projection is delivered under.
//
// Deliberately NOT `.ndjson`: a collector that globs for the verifiable export must not pick up a projection, because the two look
// alike and only one of them can be verified.
func siemFormatFilename(format string) string {
	if format == "cef" {
		return ".cef"
	}
	return ".ecs.ndjson"
}

// siemContentType is what the projection is served as.
func siemContentType(format string) string {
	if format == "cef" {
		return "text/plain; charset=utf-8"
	}
	return "application/x-ndjson; charset=utf-8"
}

// siemProjectionNote explains, in the response, what the file does not contain.
func siemProjectionNote(format string) string {
	label := strings.ToUpper(format)
	return fmt.Sprintf("%s projection: the hash chain is NOT included, so this file cannot verify the audit log - "+
		"use format=ndjson for the verifiable export", label)
}
