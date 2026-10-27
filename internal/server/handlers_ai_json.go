package server

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/pkg/protocol"
)

// compactToolResultJSON minifies tool output for the model: drop empty
// fields, no indent, and cap runaway payloads so free/small models don't
// drown in tokens.
// buildToolEvent renders the SSE "tool" frame. The frame used to be built with
// Sprintf, which embedded the tool name and the result unescaped: a model-supplied
// name containing a quote, or a tool whose output is not JSON (compactToolResultJSON
// falls back to the raw text), produced an INVALID JSON frame the client could not
// parse. Non-JSON results are now carried as a JSON string instead.
func buildToolEvent(id, name, result string) string {
	type toolEvent struct {
		ID     string          `json:"id"`
		Name   string          `json:"name"`
		Result json.RawMessage `json:"result"`
	}
	payload := toolEvent{ID: id, Name: name}
	if json.Valid([]byte(result)) && result != "" {
		payload.Result = json.RawMessage(result)
	} else {
		quoted, err := json.Marshal(result)
		if err != nil {
			payload.Result = json.RawMessage(`""`)
		} else {
			payload.Result = json.RawMessage(quoted)
		}
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return `{"error":"tool event marshal failed"}`
	}
	return string(b)
}

func compactToolResultJSON(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return raw
	}
	var v interface{}
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return truncateStr(raw, AIToolResultTruncLen*4)
	}
	b, err := json.Marshal(stripEmptyJSON(v))
	if err != nil {
		return truncateStr(raw, AIToolResultTruncLen*4)
	}
	out := string(b)
	limit := AIToolResultTruncLen * 8
	if len(out) > limit {
		return compactOversizedToolJSON(v, len(out), limit)
	}
	return out
}

func compactOversizedToolJSON(value interface{}, originalBytes, limit int) string {
	for _, budget := range []struct{ items, text int }{{32, 2000}, {16, 1200}, {8, 700}, {4, 400}, {2, 240}, {1, 160}} {
		payload := map[string]interface{}{
			"data": summarizeToolJSONValue(value, budget.items, budget.text),
			"_meta": map[string]interface{}{
				"partial":        true,
				"original_bytes": originalBytes,
				"note":           "tool result reduced to fit the AI context; request a narrower query for full detail",
			},
		}
		if encoded, err := json.Marshal(payload); err == nil && len(encoded) <= limit {
			return string(encoded)
		}
	}
	preview := truncateStr(string(mustJSON(value)), max(256, limit/2))
	encoded, _ := json.Marshal(map[string]interface{}{
		"data_preview": preview,
		"_meta": map[string]interface{}{
			"partial": true, "original_bytes": originalBytes,
			"note": "tool result reduced to fit the AI context; request a narrower query for full detail",
		},
	})
	return string(encoded)
}

func summarizeToolJSONValue(value interface{}, maxItems, maxText int) interface{} {
	switch typed := value.(type) {
	case map[string]interface{}:
		out := make(map[string]interface{}, len(typed))
		for key, item := range typed {
			out[key] = summarizeToolJSONValue(item, maxItems, maxText)
		}
		return out
	case []interface{}:
		end := len(typed)
		if end > maxItems {
			end = maxItems
		}
		out := make([]interface{}, 0, end)
		for _, item := range typed[:end] {
			out = append(out, summarizeToolJSONValue(item, maxItems, maxText))
		}
		return out
	case string:
		return truncateStr(typed, maxText)
	default:
		return typed
	}
}

func mustJSON(value interface{}) []byte {
	encoded, _ := json.Marshal(value)
	return encoded
}

func stripEmptyJSON(v interface{}) interface{} {
	switch t := v.(type) {
	case map[string]interface{}:
		out := make(map[string]interface{}, len(t))
		for k, val := range t {
			sv := stripEmptyJSON(val)
			if sv == nil || sv == "" {
				continue
			}
			out[k] = sv
		}
		return out
	case []interface{}:
		out := make([]interface{}, 0, len(t))
		for _, item := range t {
			out = append(out, stripEmptyJSON(item))
		}
		return out
	default:
		return t
	}
}

func implantIsStale(a db.Implant) bool {
	if !strings.EqualFold(a.Status, "online") {
		return false
	}
	window := 5 * time.Minute
	if a.CurrentInterval > 0 {
		d := time.Duration(a.CurrentInterval*3) * time.Second
		if d > window {
			window = d
		}
	}
	if a.LastSeen.IsZero() {
		return true
	}
	return time.Since(a.LastSeen) > window
}

// aiCollectionTypes is the whitelist for queue_collection (quiet recon).
var aiCollectionTypes = map[string]string{
	"screenshot": protocol.TaskTypeScreenshot,
	"ps":         protocol.TaskTypePS,
	"netstat":    protocol.TaskTypeNetstat,
	"av":         protocol.TaskTypeAV,
	"users":      protocol.TaskTypeUsers,
	"drives":     protocol.TaskTypeDrives,
	"services":   protocol.TaskTypeServices,
	"beacon_now": protocol.TaskTypeBeaconNow,
}
