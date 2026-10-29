package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ReadOnlyTools wires the tools that map onto this product's existing GET endpoints.
//
// Every tool is a thin proxy on purpose: the endpoint already enforces tenant scope, permission and rate limiting, and a tool that
// re-implemented any of that would be a second authorization path to keep in step - the exact duplication this codebase has paid
// for repeatedly (ETag comparison, HTTP client wrappers, the outbound-client guard).
type Client struct {
	baseURL string
	cookie  string
	http    *http.Client
}

// NewClient builds a read-only client. The cookie is the operator's own session cookie: these tools have no identity of their
// own, so they can never see more than the operator who started them.
func NewClient(baseURL, cookie string) *Client {
	return &Client{
		baseURL: strings.TrimSuffix(baseURL, "/"),
		cookie:  cookie,
		http:    &http.Client{Timeout: 20 * time.Second},
	}
}

// get performs one GET and decodes the envelope's data field.
func (c *Client) get(ctx context.Context, path string, query url.Values, out interface{}) error {
	target := c.baseURL + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return fmt.Errorf("could not build the request for %s", path)
	}
	if c.cookie != "" {
		request.Header.Set("Cookie", c.cookie)
	}
	response, err := c.http.Do(request)
	if err != nil {
		return fmt.Errorf("the console did not answer (%s)", path)
	}
	defer func() { _ = response.Body.Close() }()

	body, readErr := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if readErr != nil {
		return fmt.Errorf("the console's answer could not be read (%s)", path)
	}
	// The status is read before the body: a 401 with a valid envelope shape would otherwise look like data.
	switch response.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("the console refused this tool's credentials (HTTP %d) - the cookie is missing, expired, or lacks the permission", response.StatusCode)
	default:
		return fmt.Errorf("the console answered HTTP %d for %s", response.StatusCode, path)
	}

	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return fmt.Errorf("the console's answer was not the expected envelope (%s)", path)
	}
	if len(envelope.Data) == 0 {
		return fmt.Errorf("the console's answer carried no data (%s)", path)
	}
	if err := json.Unmarshal(envelope.Data, out); err != nil {
		return fmt.Errorf("the console's answer did not match the expected shape (%s)", path)
	}
	return nil
}

// RegisterReadOnlyTools wires the tool set onto a server.
//
// The descriptions are what a model reads to decide whether to call a tool, so each one says what the tool answers rather than what
// it fetches.
func RegisterReadOnlyTools(s *Server, client *Client) error {
	tools := []struct {
		tool    Tool
		handler Handler
	}{
		{
			tool: Tool{
				Name: "list_sessions",
				Description: "List beacon sessions, optionally only the online ones. Answers 'which hosts are up right now' - " +
					"the session list, not the task history.",
				InputSchema: json.RawMessage(`{"type":"object","properties":{"online_only":{"type":"boolean"},"limit":{"type":"integer","minimum":1,"maximum":200}},"additionalProperties":false}`),
			},
			handler: func(arguments map[string]interface{}) (string, interface{}, error) {
				onlineOnly, _ := arguments["online_only"].(bool)
				query := url.Values{}
				if onlineOnly {
					query.Set("status", "online")
				}
				query.Set("page_size", "100")
				var payload struct {
					Agents []struct {
						ID       string `json:"id"`
						Hostname string `json:"hostname"`
						Status   string `json:"status"`
						OS       string `json:"os"`
						Username string `json:"username"`
						LastSeen string `json:"last_seen"`
					} `json:"agents"`
					Total int `json:"total"`
				}
				ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
				defer cancel()
				if err := client.get(ctx, "/api/agents", query, &payload); err != nil {
					return "", nil, err
				}
				var summary strings.Builder
				fmt.Fprintf(&summary, "%d session(s) (of %d):\n", len(payload.Agents), payload.Total)
				for _, agent := range payload.Agents {
					fmt.Fprintf(&summary, "- %s (%s) %s %s last seen %s\n",
						agent.Hostname, agent.ID, agent.Status, agent.OS, agent.LastSeen)
				}
				return strings.TrimSpace(summary.String()), payload.Agents, nil
			},
		},
		{
			tool: Tool{
				Name: "failure_signatures",
				Description: "The dominant failure SHAPES in a window, normalised so that four 401s read as one problem, with the " +
					"hosts affected and the busiest host per shape. Answers 'is this one problem or many'.",
				InputSchema: json.RawMessage(`{"type":"object","properties":{"since":{"type":"string"},"engagement":{"type":"string"}},"additionalProperties":false}`),
			},
			handler: func(arguments map[string]interface{}) (string, interface{}, error) {
				query := url.Values{}
				if since, ok := arguments["since"].(string); ok && since != "" {
					query.Set("since", since)
				}
				if engagement, ok := arguments["engagement"].(string); ok && engagement != "" {
					query.Set("engagement", engagement)
				}
				var payload struct {
					TaskFailures int `json:"task_failures"`
					Shapes       []struct {
						Signature     string   `json:"signature"`
						Count         int      `json:"count"`
						Agents        int      `json:"agents_affected"`
						TopAgentHost  string   `json:"top_agent_host"`
						TopAgentCount int      `json:"top_agent_failures"`
						Reasons       []string `json:"reasons"`
					} `json:"task_signatures"`
				}
				ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
				defer cancel()
				if err := client.get(ctx, "/api/stats/failures/signatures", query, &payload); err != nil {
					return "", nil, err
				}
				var summary strings.Builder
				fmt.Fprintf(&summary, "%d failure(s) in %d shape(s):\n", payload.TaskFailures, len(payload.Shapes))
				for _, shape := range payload.Shapes {
					fmt.Fprintf(&summary, "- %q x%d on %d host(s), busiest %s (%d) [%s]\n",
						shape.Signature, shape.Count, shape.Agents, shape.TopAgentHost, shape.TopAgentCount,
						strings.Join(shape.Reasons, ","))
				}
				return strings.TrimSpace(summary.String()), payload.Shapes, nil
			},
		},
		{
			tool: Tool{
				Name: "queue_depth",
				Description: "Per-beacon backlog: pending and running counts and whether a beacon has stopped consuming work. " +
					"Answers 'which host is not draining', which the task list cannot.",
				InputSchema: json.RawMessage(`{"type":"object","properties":{"engagement":{"type":"string"}},"additionalProperties":false}`),
			},
			handler: func(arguments map[string]interface{}) (string, interface{}, error) {
				query := url.Values{}
				if engagement, ok := arguments["engagement"].(string); ok && engagement != "" {
					query.Set("engagement", engagement)
				}
				var payload struct {
					Queued        int `json:"queued"`
					StalledAgents int `json:"stalled_agents"`
					Agents        []struct {
						Hostname      string `json:"hostname"`
						Pending       int    `json:"pending"`
						Running       int    `json:"running"`
						OldestPending int64  `json:"oldest_pending_seconds"`
						Stalled       bool   `json:"stalled"`
					} `json:"agents"`
				}
				ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
				defer cancel()
				if err := client.get(ctx, "/api/stats/tasks/queue", query, &payload); err != nil {
					return "", nil, err
				}
				var summary strings.Builder
				fmt.Fprintf(&summary, "%d task(s) queued, %d beacon(s) not draining:\n", payload.Queued, payload.StalledAgents)
				for _, agent := range payload.Agents {
					marker := ""
					if agent.Stalled {
						marker = " STALLED"
					}
					fmt.Fprintf(&summary, "- %s pending=%d running=%d oldest=%ds%s\n",
						agent.Hostname, agent.Pending, agent.Running, agent.OldestPending, marker)
				}
				return strings.TrimSpace(summary.String()), payload.Agents, nil
			},
		},
		{
			tool: Tool{
				Name: "session_timeline",
				Description: "One host's activity in order: tasks, screenshots, credentials and status changes, newest first. " +
					"Answers 'what happened on this host'.",
				InputSchema: json.RawMessage(`{"type":"object","properties":{"agent_id":{"type":"string"},"limit":{"type":"integer","minimum":1,"maximum":200}},"required":["agent_id"],"additionalProperties":false}`),
			},
			handler: func(arguments map[string]interface{}) (string, interface{}, error) {
				agentID, _ := arguments["agent_id"].(string)
				if agentID == "" {
					return "", nil, fmt.Errorf("agent_id is required")
				}
				query := url.Values{}
				query.Set("limit", "100")
				var payload struct {
					Events []struct {
						Time   string `json:"time"`
						Kind   string `json:"kind"`
						Title  string `json:"title"`
						Detail string `json:"detail"`
					} `json:"events"`
				}
				ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
				defer cancel()
				if err := client.get(ctx, "/api/agents/"+url.PathEscape(agentID)+"/timeline", query, &payload); err != nil {
					return "", nil, err
				}
				var summary strings.Builder
				fmt.Fprintf(&summary, "%d event(s) for %s:\n", len(payload.Events), agentID)
				for _, event := range payload.Events {
					fmt.Fprintf(&summary, "- %s %s %s %s\n", event.Time, event.Kind, event.Title, event.Detail)
				}
				return strings.TrimSpace(summary.String()), payload.Events, nil
			},
		},
	}
	for _, entry := range tools {
		if err := s.Register(entry.tool, entry.handler); err != nil {
			return err
		}
	}
	return nil
}
