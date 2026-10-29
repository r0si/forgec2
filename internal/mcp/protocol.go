package mcp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"sort"
)

// Serve reads newline-delimited JSON-RPC from in and writes responses to the server's writer until in is exhausted.
//
// Stdio framing is newline-delimited JSON in the revision this implements; there is no Content-Length header. A malformed line is
// answered with a parse error rather than ending the loop, because one bad frame from a client should not take the session down.
func (s *Server) Serve(in io.Reader) error {
	reader := bufio.NewReader(in)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			s.handleLine(line)
		}
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}

// handleLine dispatches one frame and writes at most one response.
func (s *Server) handleLine(line []byte) {
	var request Request
	if err := json.Unmarshal(line, &request); err != nil {
		s.write(Response{JSONRPC: "2.0", Error: &Error{Code: CodeParseError, Message: "invalid JSON-RPC frame"}})
		return
	}
	// A notification has no id and expects no answer; `notifications/initialized` is the one every client sends.
	if len(request.ID) == 0 {
		return
	}
	response := Response{JSONRPC: "2.0", ID: request.ID}

	switch request.Method {
	case "initialize":
		response.Result = map[string]interface{}{
			"protocolVersion": ProtocolVersion,
			"capabilities":    map[string]interface{}{"tools": map[string]interface{}{}},
			"serverInfo":      map[string]interface{}{"name": s.name, "version": s.version},
		}
	case "tools/list":
		response.Result = map[string]interface{}{"tools": s.Tools()}
	case "tools/call":
		result, rpcErr := s.callTool(request.Params)
		if rpcErr != nil {
			response.Error = rpcErr
		} else {
			response.Result = result
		}
	case "ping":
		response.Result = map[string]interface{}{}
	default:
		response.Error = &Error{Code: CodeMethodNotFound, Message: "unknown method: " + request.Method}
	}
	s.write(response)
}

// callTool validates the arguments and runs the handler, converting a tool failure into an MCP result with isError set rather than
// a protocol error: the model should see "the endpoint said 404", not "the transport broke".
func (s *Server) callTool(raw json.RawMessage) (interface{}, *Error) {
	var params struct {
		Name      string                 `json:"name"`
		Arguments map[string]interface{} `json:"arguments"`
	}
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, &Error{Code: CodeInvalidParams, Message: "invalid tools/call parameters"}
	}
	entry, ok := s.tools[params.Name]
	if !ok {
		return nil, &Error{Code: CodeInvalidParams, Message: "unknown tool: " + params.Name}
	}
	text, structured, err := entry.handler(params.Arguments)
	if err != nil {
		return map[string]interface{}{
			"content": []map[string]interface{}{{"type": "text", "text": err.Error()}},
			"isError": true,
		}, nil
	}
	content := []map[string]interface{}{{"type": "text", "text": text}}
	result := map[string]interface{}{"content": content}
	if structured != nil {
		encoded, marshalErr := json.Marshal(structured)
		if marshalErr == nil {
			result["structuredContent"] = json.RawMessage(encoded)
		}
	}
	return result, nil
}

// write emits one frame. Encoding failures are unprintable here (the writer is a pipe), so the error is dropped deliberately - the
// only thing a second failure could do is lose the frame twice.
func (s *Server) write(response Response) {
	encoded, err := json.Marshal(response)
	if err != nil {
		return
	}
	_, _ = s.out.Write(append(encoded, '\n'))
}

// SortedArguments renders a tool's arguments deterministically, for error messages and audit details.
func SortedArguments(arguments map[string]interface{}) string {
	keys := make([]string, 0, len(arguments))
	for key := range arguments {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := ""
	for _, key := range keys {
		if out != "" {
			out += ", "
		}
		out += fmt.Sprintf("%s=%v", key, arguments[key])
	}
	return out
}
