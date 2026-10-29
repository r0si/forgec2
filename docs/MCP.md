# ForgeC2 MCP server

`forgec2-mcp` exposes this console's **read-only** views to an MCP client (Claude Desktop, any agent runtime that speaks the
protocol) over stdio. It is a separate binary: the server does not know it exists, so nothing here can widen the API's
authorization surface.

```sh
go build -o forgec2-mcp.exe ./cmd/forgec2-mcp
./forgec2-mcp.exe -base-url http://127.0.0.1:8000 -cookie "forgec2_session=..."
```

`-cookie` is the operator's own session cookie. **The tool set can never see more than the person who started it** — there is no
service identity to leak and no second authorization path to keep in step with the API's.

## Tools

| Tool | Answers | Reads |
|---|---|---|
| `list_sessions` | which hosts are up right now | `GET /api/agents` |
| `session_timeline` | what happened on this host, in order | `GET /api/agents/{id}/timeline` |
| `failure_signatures` | is this one problem or many | `GET /api/stats/failures/signatures` |
| `queue_depth` | which beacon is not draining its queue | `GET /api/stats/tasks/queue` |
| `build_environment` | why a build would fail here, before starting one | `GET /api/generate/toolchain` |
| `playbook_runs` | where a post-exploitation path stopped, and what may run next | `GET /api/playbooks/runs[/{id}]` |

Every tool is a **thin proxy**. Tenant scope, permissions and rate limits are enforced by the endpoint that already owns them;
re-implementing any of that here would create the duplication this codebase has paid for repeatedly.

## Write operations are absent on purpose

Adding them later must go through the same approval chain the AI copilot's tools use, not through a new path with its own idea of
risk. A tool that can dispatch a task from a chat window is a different security decision from one that can read a queue.

## Failure behaviour

A rejected cookie, a missing endpoint or a malformed answer becomes a **tool error** (`isError: true` with the reason in the
text), not a protocol error — the model should see *"the console refused this tool's credentials"*, not *"the transport broke"*.
Only genuinely malformed JSON-RPC (unknown method, unknown tool, unparseable frame) is a protocol error.
