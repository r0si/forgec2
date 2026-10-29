# Documentation

Where to read, by what you are trying to do. Every page in this directory is listed here; the test
`TestDocsIndexListsEveryPage` fails when one is added without an entry — a document nobody can find is a document
nobody reads.

## Using the console

| Page | What it answers |
|---|---|
| [CAPABILITY_MATRIX.md](CAPABILITY_MATRIX.md) | Which task types exist, per OS, generated from the task registry |
| [COMMAND_REFERENCE.md](COMMAND_REFERENCE.md) | Every task type's parameters, generated from its spec |
| [TERMINOLOGY.md](TERMINOLOGY.md) | The words this product uses, and what they mean here |
| [REMOTE_DESKTOP.md](REMOTE_DESKTOP.md) | Screen control: how it is negotiated and what it needs |
| [FILE_INTELLIGENCE.md](FILE_INTELLIGENCE.md) | File listing, download and the intelligence around them |
| [LISTENER_TLS.md](LISTENER_TLS.md) | Certificate handling for listeners |

## Running it

| Page | What it answers |
|---|---|
| [DEPLOY.md](DEPLOY.md) | Deploying the server and the console bundle |
| [CAPACITY.md](CAPACITY.md) | How many agents/rows the storage and the caches are sized for |
| [AUDIT_INTEGRITY.md](AUDIT_INTEGRITY.md) | What the audit chain detects, and what it does not |
| [MATURITY_BLUEPRINT.md](MATURITY_BLUEPRINT.md) | The engineering standards this codebase holds itself to |

## Handing evidence to someone else

| Page | What it answers |
|---|---|
| [VERIFIERS.md](VERIFIERS.md) | The three offline verifiers, their shared exit codes, and what none of them can prove |
| [DIAGNOSTICS.md](DIAGNOSTICS.md) | The endpoints that answer questions and change nothing, and the few that do |
| [MCP.md](MCP.md) | The read-only MCP server: what an external assistant may see, and why it cannot write |
| [PLAYBOOKS.md](PLAYBOOKS.md) | The playbook state machine: which steps may run, and what a failure settles |
| [DIRECT_SYSCALLS.md](DIRECT_SYSCALLS.md) | The syscall parsers that exist, what is deliberately not wired, and the two switching options |
| [HANDOVER_BUNDLE.md](HANDOVER_BUNDLE.md) | Producing and checking an engagement handover |
| [ENGAGEMENT_CLOSEOUT.md](ENGAGEMENT_CLOSEOUT.md) | The last hour of an exercise: stop work, assemble, verify, hand over |
| [TASK_RETRIES.md](TASK_RETRIES.md) | Which task types are re-issued automatically after a failure, and why the rest never are |
| [AUDIT_EXPORT.md](AUDIT_EXPORT.md) | Verifying an exported audit log |
| [PLUGIN_AUTHORING.md](PLUGIN_AUTHORING.md) | Writing a plugin, and checking one you received |

## Extending it

| Page | What it answers |
|---|---|
| [PLUGIN_TIERS.md](PLUGIN_TIERS.md) | Tier guarantees and quotas: what a plugin may assume |
| [AGENT_MODULES.md](AGENT_MODULES.md) | The agent's module layout and how to add one |
| [python-api.md](python-api.md) | The Python scripting surface |
| [TRANSPORT_E2E.md](TRANSPORT_E2E.md) | The transport end-to-end checks and how to run them |

## The console frontend

| Page | What it answers |
|---|---|
| [FRONTEND_CONVENTIONS.md](FRONTEND_CONVENTIONS.md) | How pages, hooks and API calls are structured |
| [frontend-style-conventions.md](frontend-style-conventions.md) | Tokens, spacing and component conventions |
| [FRONTEND_PERF.md](FRONTEND_PERF.md) | The bundle and first-paint budgets, and what spends them |
| [FRONTEND_API_PATHS.md](FRONTEND_API_PATHS.md) | The frontend/backend path contract |
| [UIUX_AUDIT.md](UIUX_AUDIT.md) | The recorded interface audit and its open items |
| [frontend-empty-state-backlog.md](frontend-empty-state-backlog.md) | Empty-state work still outstanding |
