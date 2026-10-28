package server

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/forgec2/forgec2/internal/db"
)

// ── JSON structures ───────────────────────────────────────────────────────

type chatMessage struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []toolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

type toolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function toolCallFunc `json:"function"`
}

type toolCallFunc struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type chatRequest struct {
	Model      string        `json:"model"`
	Messages   []chatMessage `json:"messages"`
	Stream     bool          `json:"stream"`
	Tools      []toolDef     `json:"tools,omitempty"`
	ToolChoice interface{}   `json:"tool_choice,omitempty"`
	// Output token cap; 0 = provider default (claude falls back to
	// ClaudeMaxTokens). Set by one-shot helpers, left unset by the
	// streaming conversation loop.
	MaxTokens int `json:"max_tokens,omitempty"`
	// OpenRouter / DeepSeek: ask the model to emit thinking tokens.
	Reasoning *aiReasoningHint `json:"reasoning,omitempty"`
}

type toolDef struct {
	Type     string      `json:"type"`
	Function toolFuncDef `json:"function"`
}

type toolFuncDef struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Parameters  map[string]interface{} `json:"parameters"`
}

// ── Tool Definitions ──────────────────────────────────────────────────────

func (s *Server) buildTools() []toolDef {
	allowExec := s.aiExecutionEnabled()
	execDesc := "Execute a command on the specified agent."
	if allowExec {
		execDesc += " When allow_execute is enabled, non-sensitive commands execute immediately (optional wait_for_result up to 60s); sensitive commands (mimikatz/dcsync/secretsdump etc.) always require human approval and return pending_approval."
	} else {
		execDesc += " The command is queued and requires a human operator to approve it before the agent executes it. Returns pending_approval; use get_agent_tasks later to check the result."
	}
	return []toolDef{
		{
			Type: "function",
			Function: toolFuncDef{
				Name:        "list_agents",
				Description: "List implants. Filter with status (online/offline), os, query (hostname/IP/username/id), elevated=true. Returns id, hostname, IP, OS, user, elevated, integrity, sleep, last_seen, stale (missed check-in). Prefer status=online for 'who is up'.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"status": map[string]string{
							"type":        "string",
							"description": "online | offline | all (default all)",
						},
						"os": map[string]string{
							"type":        "string",
							"description": "OS substring filter, e.g. windows, linux",
						},
						"query": map[string]string{
							"type":        "string",
							"description": "Search hostname, IP, username, or agent id",
						},
						"elevated": map[string]interface{}{
							"type":        "boolean",
							"description": "If true, only elevated (admin/root) implants",
						},
						"limit": map[string]interface{}{
							"type":        "integer",
							"description": "Max results (1-50, default 30)",
						},
					},
				},
			},
		},
		{
			Type: "function",
			Function: toolFuncDef{
				Name:        "get_agent_detail",
				Description: "Get agent details including system info, privileges, and task stats",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"agent_id": map[string]string{
							"type":        "string",
							"description": "Agent ID or hostname",
						},
					},
					"required": []string{"agent_id"},
				},
			},
		},
		{
			Type: "function",
			Function: toolFuncDef{
				Name:        "execute_command",
				Description: execDesc,
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"agent_id": map[string]string{
							"type":        "string",
							"description": "Target agent ID or hostname",
						},
						"command": map[string]string{
							"type":        "string",
							"description": "Command to execute (cmd.exe or PowerShell)",
						},
						"shell": map[string]string{
							"type":        "string",
							"description": "Shell type: cmd.exe or powershell.exe",
						},
						"wait_for_result": map[string]interface{}{
							"type":        "boolean",
							"description": "When true and allow_execute is enabled, wait up to 60s for the result. Sensitive commands ignore this flag.",
						},
					},
					"required": []string{"agent_id", "command"},
				},
			},
		},
		{
			Type: "function",
			Function: toolFuncDef{
				Name:        "get_agent_tasks",
				Description: "Get recent task list and results for a specified agent (use when execute_command timed out or wait_for_result was false)",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"agent_id": map[string]string{
							"type":        "string",
							"description": "Agent ID or hostname",
						},
					},
					"required": []string{"agent_id"},
				},
			},
		},
		{
			Type: "function",
			Function: toolFuncDef{
				Name:        "list_listeners",
				Description: "List all configured listeners and their status",
				Parameters: map[string]interface{}{
					"type":       "object",
					"properties": map[string]interface{}{},
				},
			},
		},
		{
			Type: "function",
			Function: toolFuncDef{
				Name:        "list_credentials",
				Description: "View credential vault summary (without plaintext passwords)",
				Parameters: map[string]interface{}{
					"type":       "object",
					"properties": map[string]interface{}{},
				},
			},
		},
		{
			Type: "function",
			Function: toolFuncDef{
				Name:        "get_online_operators",
				Description: "List operators currently connected to the teamserver dashboard and which agent they are viewing, if any.",
				Parameters: map[string]interface{}{
					"type":       "object",
					"properties": map[string]interface{}{},
				},
			},
		},
		{
			Type: "function",
			Function: toolFuncDef{
				Name:        "search_tasks",
				Description: "Search recent tasks by command/result text (e.g. \"mimikatz\", \"360\"). Returns matching tasks with agent, command, status, result snippet.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"query": map[string]string{
							"type":        "string",
							"description": "Search keyword (case-insensitive substring)",
						},
						"limit": map[string]interface{}{
							"type":        "integer",
							"description": "Max results (1-50, default 10)",
						},
					},
					"required": []string{"query"},
				},
			},
		},
		{
			Type: "function",
			Function: toolFuncDef{
				Name:        "get_timeline",
				Description: "Get unified activity timeline for an agent (tasks, screenshots, credentials, status changes) merged by time. Up to 100 most recent events.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"agent_id": map[string]string{
							"type":        "string",
							"description": "Agent ID",
						},
						"limit": map[string]interface{}{
							"type":        "integer",
							"description": "Max events (1-100, default 30)",
						},
					},
					"required": []string{"agent_id"},
				},
			},
		},
		{
			Type: "function",
			Function: toolFuncDef{
				Name:        "query_ioc",
				Description: "Query extracted indicators of compromise (IPs, domains, URLs, file hashes) from recent task results and recon data. Returns top indicators by occurrence.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"days": map[string]interface{}{
							"type":        "integer",
							"description": "Look-back window in days (1-365, default 30)",
						},
						"type": map[string]string{
							"type":        "string",
							"description": "Filter by type: ipv4, domain, url, md5, sha1, sha256 (empty = all)",
						},
					},
				},
			},
		},
		{
			Type: "function",
			Function: toolFuncDef{
				Name:        "list_macros",
				Description: "List available command macros (recorded command sequences)",
				Parameters: map[string]interface{}{
					"type":       "object",
					"properties": map[string]interface{}{},
				},
			},
		},
		{
			Type: "function",
			Function: toolFuncDef{
				Name:        "run_macro",
				Description: "Execute a command macro on one or more agents. Each agent runs the macro steps sequentially. Requires allow_execute; otherwise returns pending_approval error.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"macro_id": map[string]interface{}{
							"type":        "integer",
							"description": "Macro ID (preferred)",
						},
						"macro_name": map[string]string{
							"type":        "string",
							"description": "Macro name (alternative to macro_id)",
						},
						"agent_ids": map[string]interface{}{
							"type":        "array",
							"description": "Target agent IDs (max 10)",
							"items":       map[string]string{"type": "string"},
						},
					},
					"required": []string{"agent_ids"},
				},
			},
		},
		{
			Type: "function",
			Function: toolFuncDef{
				Name:        "create_automation_rule",
				Description: "Create an automation rule that triggers on events (agent.checkin, agent.disconnect, task.complete, task.fail, credential.found). The rule's action creates a task or runs a macro automatically. Requires allow_execute; returns the created rule.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"name": map[string]string{
							"type":        "string",
							"description": "Rule name",
						},
						"event_type": map[string]string{
							"type":        "string",
							"description": "Trigger event: agent.checkin, agent.disconnect, task.complete, task.fail, credential.found",
						},
						"command": map[string]string{
							"type":        "string",
							"description": "Shell command to run when triggered (alternative to macro)",
						},
						"macro_id": map[string]interface{}{
							"type":        "integer",
							"description": "Macro ID to run when triggered (alternative to command)",
						},
					},
					"required": []string{"name", "event_type"},
				},
			},
		},
		{
			Type: "function",
			Function: toolFuncDef{
				Name:        "get_attack_surface",
				Description: "Get attack surface for an agent: host profile, credentials nearby, lateral movement history and p2p peers. Use this to recommend the next step in a red-team engagement.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"agent_id": map[string]string{
							"type":        "string",
							"description": "Agent ID or hostname",
						},
					},
					"required": []string{"agent_id"},
				},
			},
		},
		{
			Type: "function",
			Function: toolFuncDef{
				Name:        "get_task_detail",
				Description: "Get the FULL untruncated result/error of one task by ID. Use this to diagnose failed commands or read EDR interception messages that get_agent_tasks truncates.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"task_id": map[string]interface{}{
							"type":        "integer",
							"description": "Task ID",
						},
					},
					"required": []string{"task_id"},
				},
			},
		},
		{
			Type: "function",
			Function: toolFuncDef{
				Name:        "execute_command_bulk",
				Description: "Execute the same command on multiple agents at once (max 20). Without allow_execute every task is queued as pending_approval for human review; with allow_execute non-sensitive commands run immediately. Returns per-agent task ids.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"agent_ids": map[string]interface{}{
							"type":        "array",
							"description": "Target agent IDs or hostnames (max 20)",
							"items":       map[string]string{"type": "string"},
						},
						"command": map[string]string{
							"type":        "string",
							"description": "Command to run on each agent",
						},
						"shell": map[string]string{
							"type":        "string",
							"description": "Shell type: cmd.exe or powershell.exe",
						},
					},
					"required": []string{"agent_ids", "command"},
				},
			},
		},
		{
			Type: "function",
			Function: toolFuncDef{
				Name:        "query_bloodhound",
				Description: "Query the latest BloodHound collection summary (users/computers/groups/sessions/DA counts). Use to answer attack-path questions like 'how do we reach Domain Admin'.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"agent_id": map[string]string{
							"type":        "string",
							"description": "Optional: filter collections by originating agent",
						},
					},
				},
			},
		},
		{
			Type: "function",
			Function: toolFuncDef{
				Name:        "get_coverage_gaps",
				Description: "Get MITRE ATT&CK coverage gaps: techniques mapped by ForgeC2 but NOT yet exercised in tasks, grouped by tactic. Combine with known host/cred context to recommend next techniques.",
				Parameters: map[string]interface{}{
					"type":       "object",
					"properties": map[string]interface{}{},
				},
			},
		},
		{
			Type: "function",
			Function: toolFuncDef{
				Name:        "create_plugin",
				Description: "Write a ForgeC2 plugin package (manifest + entry file) from the operator's description, validate it, and register it so it appears on the Plugins page. The package is written and registered but NOT executed - use run_plugin to check it. Plugin ABI: read the JSON input from stdin, then print ONE JSON object to stdout, either {\"success\":true,\"output\":\"text\"} or {\"success\":false,\"error\":\"reason\"}; anything else on stdout is a parse error, not a result.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"name": map[string]string{
							"type":        "string",
							"description": "Lowercase name, 2-64 chars, letters/digits/-/_ (becomes the directory name)",
						},
						"description": map[string]string{
							"type":        "string",
							"description": "One sentence for the plugin list",
						},
						"type": map[string]string{
							"type":        "string",
							"description": "command (default), hook or report",
						},
						"interpreter": map[string]string{
							"type":        "string",
							"description": "python, python3, powershell, pwsh, bash, sh or go",
						},
						"code": map[string]string{
							"type":        "string",
							"description": "Entry file contents (max 64 KiB). Read JSON from stdin, print one JSON object to stdout.",
						},
						"category": map[string]string{
							"type":        "string",
							"description": "recon, privesc, lateral, report, automation or utility",
						},
					},
					"required": []string{"name", "description", "interpreter", "code"},
				},
			},
		},
		{
			Type: "function",
			Function: toolFuncDef{
				Name:        "save_engagement_note",
				Description: "Persist a durable engagement note into long-term memory (appended with timestamp). Notes are injected into every future conversation. Use for key findings like 'DC01 allows DCSync from CA-01' or operator constraints. mode=replace overwrites ALL notes.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"note": map[string]string{
							"type":        "string",
							"description": "Note text (keep it short and factual)",
						},
						"mode": map[string]string{
							"type":        "string",
							"description": "\"append\" (default) or \"replace\" (wipes all previous notes)",
						},
					},
					"required": []string{"note"},
				},
			},
		},
		{
			Type: "function",
			Function: toolFuncDef{
				Name:        "list_pending_tasks",
				Description: "List tasks awaiting execution or approval (status pending/pending_approval), optionally filtered by creator. Use this to review the approval queue and recommend which tasks are safe to approve.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"creator": map[string]string{
							"type":        "string",
							"description": "Filter by creator: \"ai\", \"human\" (default: all)",
						},
						"limit": map[string]interface{}{
							"type":        "integer",
							"description": "Max results (1-50, default 20)",
						},
					},
				},
			},
		},
		{
			Type: "function",
			Function: toolFuncDef{
				Name:        "bulk_task_action",
				Description: "Cancel or approve multiple AI-created pending tasks at once. action=cancel always works for AI-created tasks. action=approve requires allow_execute AND only works on non-sensitive commands - sensitive commands (mimikatz/dcsync etc.) always need a human operator, preserving the two-man rule. Human-created tasks are never touched.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"task_ids": map[string]interface{}{
							"type":        "array",
							"description": "Task IDs to act on (max 50)",
							"items":       map[string]string{"type": "integer"},
						},
						"action": map[string]string{
							"type":        "string",
							"description": "\"approve\" (queue for execution) or \"cancel\" (abort)",
						},
					},
					"required": []string{"task_ids", "action"},
				},
			},
		},
		{
			Type: "function",
			Function: toolFuncDef{
				Name:        "run_plugin",
				Description: "Run a registered command plugin and return its output, to check that a plugin you wrote actually works before reporting back. The plugin runs as a child process on the server with the executor's per-plugin timeout and output cap. Only command plugins can be run directly; hooks fire on events and reports render on demand.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"name": map[string]string{
							"type":        "string",
							"description": "Registered plugin name (see the Plugins page or a previous create_plugin result)",
						},
						"agent_id": map[string]string{
							"type":        "string",
							"description": "Optional: the agent the plugin should act on, for plugins that use one",
						},
						"params": map[string]interface{}{
							"type":        "object",
							"description": "Plugin parameters, as declared by the plugin's manifest",
						},
					},
					"required": []string{"name"},
				},
			},
		},
		{
			Type: "function",
			Function: toolFuncDef{
				Name:        "update_plugin",
				Description: "Rewrite the entry file of a plugin YOU wrote (author forgec2-ai), bumping its patch version. Use it to iterate after run_plugin shows a problem; a plugin the assistant did not author is refused, because the operator edits those. The plugin is re-registered but NOT executed - call run_plugin to check the new behaviour.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"name": map[string]string{
							"type":        "string",
							"description": "Plugin name to update",
						},
						"code": map[string]string{
							"type":        "string",
							"description": "New entry file contents (max 64 KiB). Read JSON from stdin, print one JSON object to stdout.",
						},
						"description": map[string]string{
							"type":        "string",
							"description": "Optional: new one-line description for the plugin list",
						},
						"version": map[string]string{
							"type":        "string",
							"description": "Optional: explicit version; defaults to a patch bump",
						},
					},
					"required": []string{"name", "code"},
				},
			},
		},
		{
			Type: "function",
			Function: toolFuncDef{
				Name:        "delete_plugin",
				Description: "Delete a plugin YOU wrote (author forgec2-ai): unloads it, removes its record and its package directory. A plugin the assistant did not author is refused - the installed set is the operator's inventory. Use it to clean up experiments; to iterate on a plugin keep it and use update_plugin.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"name": map[string]string{
							"type":        "string",
							"description": "Plugin name to delete",
						},
					},
					"required": []string{"name"},
				},
			},
		},
		{
			Type: "function",
			Function: toolFuncDef{
				Name:        "get_screenshot_summary",
				Description: "Summarize screenshot captures within a look-back window: total count, per-agent breakdown, latest captures. Read-only analysis of collection activity.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"agent_id": map[string]string{
							"type":        "string",
							"description": "Optional: filter by agent ID or hostname",
						},
						"days": map[string]interface{}{
							"type":        "integer",
							"description": "Look-back window in days (1-365, default 30)",
						},
					},
				},
			},
		},
		{
			Type: "function",
			Function: toolFuncDef{
				Name:        "get_keylog_summary",
				Description: "Summarize keylogger dumps within a look-back window: dump counts per agent and high-value entries (passwords, logins, emails detected via keyword scan of captured keystrokes). Read-only.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"agent_id": map[string]string{
							"type":        "string",
							"description": "Optional: filter by agent ID or hostname",
						},
						"days": map[string]interface{}{
							"type":        "integer",
							"description": "Look-back window in days (1-365, default 30)",
						},
					},
				},
			},
		},
		{
			Type: "function",
			Function: toolFuncDef{
				Name:        "generate_report",
				Description: "Generate a structured engagement report from live data (agents, tasks, credentials, listeners, IOCs, MITRE coverage) and save it to the report library. Scopes: full (everything), executive (summary+findings+recommendations), technical (agents/tasks/creds/network/iocs), coverage (MITRE gaps analysis). Returns the report id; operators view/export it on the Report page.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"scope": map[string]string{
							"type":        "string",
							"description": "full | executive | technical | coverage (default full)",
						},
						"title": map[string]string{
							"type":        "string",
							"description": "Optional report title (default auto-generated with date)",
						},
					},
				},
			},
		},
		{
			Type: "function",
			Function: toolFuncDef{
				Name:        "create_listener",
				Description: "Create and bind a new C2 listener. Requires allow_execute. Schemes: http, https, tcp, tls, dns, icmp, ssh, h2c, udp, quic. DNS listeners use dns_domain instead of port; ICMP uses icmp_addr. The returned status reflects whether the bind actually succeeded.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"name": map[string]string{
							"type":        "string",
							"description": "Listener name (must be unique)",
						},
						"scheme": map[string]string{
							"type":        "string",
							"description": "http | https | tcp | tls | dns | icmp | ssh | h2c | udp | quic",
						},
						"host": map[string]string{
							"type":        "string",
							"description": "Bind host/IP (e.g. 0.0.0.0)",
						},
						"port": map[string]interface{}{
							"type":        "integer",
							"description": "Bind port 1-65535 (0 for dns/icmp)",
						},
						"dns_domain": map[string]string{
							"type":        "string",
							"description": "DNS domain for dns-scheme listeners",
						},
						"icmp_addr": map[string]string{
							"type":        "string",
							"description": "Listen address for icmp-scheme listeners",
						},
						"notes": map[string]string{
							"type":        "string",
							"description": "Optional notes",
						},
					},
					"required": []string{"scheme"},
				},
			},
		},
		{
			Type: "function",
			Function: toolFuncDef{
				Name:        "update_listener",
				Description: "Update an existing listener: toggle enabled, change bind host/port, or rename. Requires allow_execute. Bind-address changes restart the listener.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"listener_id": map[string]interface{}{
							"type":        "integer",
							"description": "Listener ID",
						},
						"enabled": map[string]interface{}{
							"type":        "boolean",
							"description": "Enable or disable the listener",
						},
						"name": map[string]string{
							"type":        "string",
							"description": "New name",
						},
						"host": map[string]string{
							"type":        "string",
							"description": "New bind host/IP",
						},
						"port": map[string]interface{}{
							"type":        "integer",
							"description": "New bind port 1-65535",
						},
					},
					"required": []string{"listener_id"},
				},
			},
		},
		{
			Type: "function",
			Function: toolFuncDef{
				Name:        "get_situation",
				Description: "Live engagement snapshot: agent counts, OS mix of online hosts, elevated online count, listeners, pending approvals, active alerts, credential count, connected operators. Use this first for 'what's going on' / situation reports.",
				Parameters: map[string]interface{}{
					"type":       "object",
					"properties": map[string]interface{}{},
				},
			},
		},
		{
			Type: "function",
			Function: toolFuncDef{
				Name:        "get_alerts",
				Description: "List recent alerts (beacon drops, rule hits). Filter by status: active, acknowledged, resolved. Use when the operator asks what needs attention.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"status": map[string]string{
							"type":        "string",
							"description": "active | acknowledged | resolved | all (default active)",
						},
						"limit": map[string]interface{}{
							"type":        "integer",
							"description": "Max results (1-50, default 15)",
						},
					},
				},
			},
		},
		{
			Type: "function",
			Function: toolFuncDef{
				Name:        "set_sleep",
				Description: "Queue a set_sleep task to change an implant's beacon interval and jitter. interval is seconds (1-86400), jitter is 0-100 percent. Same approval rules as execute_command.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"agent_id": map[string]string{
							"type":        "string",
							"description": "Agent ID or hostname",
						},
						"interval": map[string]interface{}{
							"type":        "integer",
							"description": "Sleep interval in seconds (1-86400)",
						},
						"jitter": map[string]interface{}{
							"type":        "integer",
							"description": "Jitter percent 0-100 (default 0)",
						},
					},
					"required": []string{"agent_id", "interval"},
				},
			},
		},
		{
			Type: "function",
			Function: toolFuncDef{
				Name:        "queue_collection",
				Description: "Queue a typed collection/recon task on an agent. action: screenshot, ps, netstat, av, users, drives, services, beacon_now. Prefer this over execute_command. Same approval rules as execute_command.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"agent_id": map[string]string{
							"type":        "string",
							"description": "Agent ID or hostname",
						},
						"action": map[string]string{
							"type":        "string",
							"description": "screenshot | ps | netstat | av | users | drives | services | beacon_now",
						},
					},
					"required": []string{"agent_id", "action"},
				},
			},
		},
		{
			Type: "function",
			Function: toolFuncDef{
				Name:        "delete_listener",
				Description: "Delete a listener permanently. Requires allow_execute. Refuses if any agent still references the listener.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"listener_id": map[string]interface{}{
							"type":        "integer",
							"description": "Listener ID",
						},
					},
					"required": []string{"listener_id"},
				},
			},
		},
		{
			Type: "function",
			Function: toolFuncDef{
				Name:        "web_search",
				Description: "Search the web (proxied, allowlisted domains only). Use for MITRE, CVEs, tool docs. Returns title, snippet, url with [web: url] citations.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"query": map[string]string{"type": "string", "description": "Search query (<=200 chars)"},
						"limit": map[string]interface{}{"type": "integer", "description": "Max results 1-6 (default 3)", "minimum": 1, "maximum": 6},
					},
					"required": []string{"query"},
				},
			},
		},
	}
}

// ── Tool Execution ────────────────────────────────────────────────────────

func (s *Server) executeToolCtx(reqCtx *aiReqCtx, name string, argsJSON string) string {
	// Tolerant pre-parse: schema-conformant tools carry arrays/numbers/bools
	// (agent_ids, task_ids, port, days...) which cannot decode into
	// map[string]string. encoding/json still fills every string-valued key
	// before reporting the type error, so we ignore the error here — each
	// tool case below re-parses argsJSON into its own typed struct anyway.
	var args map[string]string
	_ = json.Unmarshal([]byte(argsJSON), &args)
	if reqCtx == nil {
		reqCtx = &aiReqCtx{}
	}
	// Context fallback: tools that accept agent_id may omit it when the
	// operator is already focused on an agent in the console. Array-valued
	// keys never land in the partial map; injectDefaultAgent re-parses the
	// full JSON and only injects when the real key is absent/empty.
	normalizedArgs := argsJSON
	if reqCtx.DefaultAgentID != "" {
		for _, key := range []string{"agent_id", "agent_ids"} {
			raw, exists := args[key]
			if !exists || strings.TrimSpace(raw) == "" || raw == "null" || raw == `[]` {
				continue
			}
			normalizedArgs = argsJSON
			goto authorize
		}
		normalizedArgs = injectDefaultAgent(name, argsJSON, reqCtx.DefaultAgentID)
	}
authorize:
	if allowed, result := s.authorizeAITool(reqCtx, name, normalizedArgs); !allowed {
		return result
	}
	// Audit every authorized tool call (read and write alike) with
	// secret-redacted arguments, so AI-driven actions are traceable in the
	// same trail as operator clicks. Approval decisions keep their own
	// ai_intent_approve/reject entries; this records the execution itself.
	s.auditAIToolCall(reqCtx, name, normalizedArgs)
	if name == "search_knowledge" {
		return s.executeAIKnowledgeSearchTool(reqCtx, normalizedArgs)
	}
	if name == "web_search" {
		return s.executeAIWebSearchTool(reqCtx, normalizedArgs)
	}
	// Writes a plugin package: admin + approval intent, and never executed here.
	if name == "create_plugin" {
		return s.aiCreatePluginTool(reqCtx, normalizedArgs)
	}
	// Runs a registered plugin: a separate permission from writing one, and the existing executor's
	// quotas and timeout apply.
	if name == "run_plugin" {
		return s.aiRunPluginTool(reqCtx, normalizedArgs)
	}
	// Rewrites an assistant-authored package in place; a plugin it did not write is refused.
	if name == "update_plugin" {
		return s.aiUpdatePluginTool(reqCtx, normalizedArgs)
	}
	// Removes an assistant-authored package (unload + record + directory), with its own permission.
	if name == "delete_plugin" {
		return s.aiDeletePluginTool(reqCtx, normalizedArgs)
	}
	return s.executeToolSwitchCtx(reqCtx, name, normalizedArgs)
}

// auditAIToolCall records one authorized AI tool invocation. Arguments go
// through summarizeAIArguments so credentials never land in the audit trail.
func (s *Server) auditAIToolCall(reqCtx *aiReqCtx, name, argsJSON string) {
	if s == nil || s.db == nil {
		return
	}
	user, target := "ai", ""
	if reqCtx != nil {
		if reqCtx.Principal.Username != "" {
			user = reqCtx.Principal.Username
		}
		target = reqCtx.RunID
		if reqCtx.SessionID != 0 {
			target = "session:" + strconv.FormatUint(uint64(reqCtx.SessionID), 10)
		}
	}
	details := name
	if target != "" {
		details = "[" + target + "] " + details
	}
	if digest := summarizeAIArguments(argsJSON); digest != "" {
		details = details + " " + digest
	}
	s.flushAuditEntries([]db.AuditLog{{
		User:     user,
		Action:   "ai_tool",
		Resource: "ai",
		Success:  true,
		Details:  truncateString(details, 1000),
	}})
}

// injectDefaultAgent returns argsJSON with the context agent filled in for
// agent-scoped tools (missing/empty agent_id, missing agent_ids for macros).
// Pure so it stays unit-testable without touching tools or the database.
func injectDefaultAgent(name string, argsJSON string, defaultAgent string) string {
	switch name {
	case "execute_command", "get_agent_tasks", "get_timeline", "get_attack_surface",
		"get_agent_detail", "query_bloodhound", "set_sleep", "queue_collection":
		var orig map[string]interface{}
		if err := json.Unmarshal([]byte(argsJSON), &orig); err == nil {
			cur, _ := orig["agent_id"].(string)
			if strings.TrimSpace(cur) == "" {
				orig["agent_id"] = defaultAgent
				if b, ok := marshalJSONSafe(orig); ok {
					return string(b)
				}
			}
		}
	case "run_macro", "execute_command_bulk":
		// Both read an agent_ids ARRAY; a bare string default would be ignored.
		var orig map[string]interface{}
		if err := json.Unmarshal([]byte(argsJSON), &orig); err == nil {
			if _, has := orig["agent_ids"]; !has {
				orig["agent_ids"] = []string{defaultAgent}
				if b, ok := marshalJSONSafe(orig); ok {
					return string(b)
				}
			}
		}
	}
	return argsJSON
}

func (s *Server) executeToolSwitch(name string, argsJSON string) string {
	return s.executeToolSwitchCtx(nil, name, argsJSON)
}
