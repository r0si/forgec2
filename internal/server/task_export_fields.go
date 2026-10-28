package server

import (
	"fmt"
	"strings"

	"github.com/forgec2/forgec2/internal/db"
)

// Column selection for the task export.
//
// An export of ten thousand rows carries the result and error text of every one of them, and the caller very
// often wants only the identifiers and the outcome - "what ran, did it work" - or only the results. `?fields=`
// lets that caller say so. Both formats honour the same list, in the caller's order, so a script and a
// spreadsheet see the same columns in the same sequence.
//
// An empty list means "every field", which keeps the endpoint's existing behaviour for every current caller.

// exportField describes one selectable column.
type exportField struct {
	name    string
	header  string
	value   func(task db.Task, agentName, engagementName string) string
	numeric bool
}

// exportFields is the ordered catalogue: this order is what `?fields=` absent produces.
var exportFields = []exportField{
	{name: "time", header: "Time", value: func(t db.Task, _, _ string) string {
		return t.CreatedAt.Format("2006-01-02 15:04:05")
	}},
	{name: "agent", header: "Agent", value: func(_ db.Task, agentName, _ string) string { return agentName }},
	{name: "type", header: "Type", value: func(t db.Task, _, _ string) string { return t.Type }},
	{name: "command", header: "Command", value: func(t db.Task, _, _ string) string { return csvSafe(t.Command) }},
	{name: "result", header: "Result", value: func(t db.Task, _, _ string) string {
		return csvSafe(truncateString(redactSensitiveResultForAuxiliary(t.Type, t.Result), CSVResultTruncLen))
	}},
	{name: "error", header: "Error", value: func(t db.Task, _, _ string) string {
		return csvSafe(truncateString(t.Error, CSVErrorTruncLen))
	}},
	{name: "status", header: "Status", value: func(t db.Task, _, _ string) string { return t.Status }},
	{name: "task_id", header: "Task ID", numeric: true, value: func(t db.Task, _, _ string) string {
		return itoaTaskID(t.ID)
	}},
	{name: "engagement", header: "Engagement", value: func(_ db.Task, _, engagementName string) string {
		return csvSafe(engagementName)
	}},
	// agent_id is selectable but not part of the default set: the CSV has always shown the hostname, and a
	// caller that wants to join on the id can now ask for it without losing the human-readable column.
	{name: "agent_id", header: "Agent ID", value: func(t db.Task, _, _ string) string { return t.AgentID }},
}

// selectExportFields resolves a `?fields=` value into the catalogue, or reports the first unknown name.
func selectExportFields(raw, format string) ([]exportField, string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return defaultExportFields(format), ""
	}
	byName := make(map[string]exportField, len(exportFields))
	for _, field := range exportFields {
		byName[field.name] = field
	}
	selected := make([]exportField, 0, 8)
	seen := make(map[string]bool, 8)
	for _, part := range strings.Split(raw, ",") {
		name := strings.ToLower(strings.TrimSpace(part))
		if name == "" {
			continue
		}
		field, ok := byName[name]
		if !ok {
			return nil, fmt.Sprintf("unknown field %q (valid: %s)", name, exportFieldNames())
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		selected = append(selected, field)
	}
	if len(selected) == 0 {
		return nil, fmt.Sprintf("no fields selected (valid: %s)", exportFieldNames())
	}
	return selected, ""
}

// defaultExportFields is the no-`?fields=` set, and it is format-aware on purpose.
//
// The CSV has always shown the hostname and nine columns; adding agent_id to it would change a contract consumers
// already parse. The JSON format is new, and a script reading JSON wants the id, so it gets both. An explicit
// `?fields=` overrides this for either format.
func defaultExportFields(format string) []exportField {
	out := make([]exportField, 0, len(exportFields))
	for _, field := range exportFields {
		if field.name == "agent_id" && format != "json" {
			continue
		}
		out = append(out, field)
	}
	return out
}

// exportFieldNames lists the valid names for an error message.
func exportFieldNames() string {
	names := make([]string, 0, len(exportFields))
	for _, field := range exportFields {
		names = append(names, field.name)
	}
	return strings.Join(names, ", ")
}

// exportFieldsTag renders the selection into the ETag: two different column sets are two different
// documents, and a client that revalidated one must not be told 304 for the other.
func exportFieldsTag(fields []exportField) string {
	names := make([]string, 0, len(fields))
	for _, field := range fields {
		names = append(names, field.name)
	}
	return strings.Join(names, "+")
}

// itoaTaskID renders a task id for a cell. Kept local so the export does not depend on a test helper.
func itoaTaskID(v uint) string {
	if v == 0 {
		return "0"
	}
	var digits []byte
	for v > 0 {
		digits = append([]byte{byte('0' + v%10)}, digits...)
		v /= 10
	}
	return string(digits)
}
