// Package mcp implements a small Model Context Protocol server that fronts this product's READ-ONLY REST surface.
//
// Why it exists: an operator's assistant is most useful when it can see what the console sees - which sessions are up, what failed,
// which beacon has stopped draining - without being handed a session token with write scope. Every tool here maps to an endpoint
// that already exists and already enforces tenant scope and permissions; nothing in this package reaches the database directly, so
// the surface cannot drift from the API's own authorization.
//
// The write side is deliberately absent. Adding it later must go through the same approval chain the AI tools use, not through a
// new path with its own idea of risk.
package mcp

import (
	"encoding/json"
	"fmt"
	"io"
)

// JSON-RPC 2.0 error codes used by MCP.
const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternalError  = -32603
)

// Request is one JSON-RPC request or notification.
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// Response is one JSON-RPC response.
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  interface{}     `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

// Error is a JSON-RPC error object.
type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return e.Message }

// ProtocolVersion is the MCP revision this server implements.
//
// Pinned rather than negotiated loosely: a client that speaks a newer revision should be told which one it got, so a tool whose
// shape differs can fail loudly instead of silently answering with the wrong schema.
const ProtocolVersion = "2024-11-05"

// Tool is one callable, with the JSON schema its caller validates against.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// Handler answers one tool call and returns text for the model plus a structured value for the caller.
type Handler func(arguments map[string]interface{}) (text string, structured interface{}, err error)

// registeredTool pairs the advertised shape with its implementation.
type registeredTool struct {
	tool    Tool
	handler Handler
}

// Server holds the tool registry and the transport.
type Server struct {
	name      string
	version   string
	tools     map[string]registeredTool
	order     []string
	out       io.Writer
	initialis bool
}

// NewServer builds an empty server; tools are added with Register.
func NewServer(name, version string, out io.Writer) *Server {
	return &Server{name: name, version: version, tools: map[string]registeredTool{}, out: out}
}

// Register adds a tool. A duplicate name is refused rather than replaced: two implementations of one name is how a client ends up
// calling something other than what it advertised.
func (s *Server) Register(tool Tool, handler Handler) error {
	if tool.Name == "" {
		return fmt.Errorf("a tool needs a name")
	}
	if handler == nil {
		return fmt.Errorf("tool %q has no handler", tool.Name)
	}
	if _, exists := s.tools[tool.Name]; exists {
		return fmt.Errorf("tool %q is already registered", tool.Name)
	}
	s.tools[tool.Name] = registeredTool{tool: tool, handler: handler}
	s.order = append(s.order, tool.Name)
	return nil
}

// Tools lists the registered tools in registration order, so a client's menu is stable across restarts.
func (s *Server) Tools() []Tool {
	out := make([]Tool, 0, len(s.order))
	for _, name := range s.order {
		out = append(out, s.tools[name].tool)
	}
	return out
}
