# ForgeC2 Python API

## Authentication

All API requests require a session cookie obtained via login.

```python
import requests

session = requests.Session()
session.post("http://localhost:8000/login", data={
    "username": "admin",
    "password": "admin"
})
```

## Endpoints

### Agents
- `GET /api/agents` — List all agents (paginated)
- `GET /agents/:id` — Get agent details (dual-use: send `Accept: application/json`)
- `POST /agents/:id/command` — Execute shell command on agent
- `POST /agents/:id/kill` — Kill agent
- `DELETE /agents/:id` — Delete agent
- `POST /agents/:id/trust` — Toggle agent trust
- `POST /agents/batch` — Batch command across agents
- `GET /agents/:id/tasks` — List agent tasks
- `GET /agents/:id/tasks/:taskId` — Get task status

### Listeners
- `GET /api/listeners` — List listeners (paginated)
- `POST /api/listeners` — Create listener
- `PUT /api/listeners/:id` — Update listener
- `DELETE /api/listeners/:id` — Delete listener
- `POST /api/listeners/:id/enable` — Enable listener
- `POST /api/listeners/:id/disable` — Disable listener

### Tasks
- `GET /tasks` — List all tasks (paginated)
- `GET /agents/:id/tasks` — List agent tasks
- `GET /agents/:id/tasks/:taskId` — Get task status

### Credentials
- `GET /credentials` — List credentials
- `POST /credentials/add` — Add credential
- `PUT /credentials/:cred_id` — Update credential
- `DELETE /credentials/:cred_id` — Delete credential

### Build & Generate
- `POST /generate/exe` — Generate Windows EXE
- `POST /generate/dll` — Generate Windows DLL
- `POST /generate/ps1` — Generate PowerShell script
- `POST /generate/linux` — Generate Linux binary
- `POST /generate/macos` — Generate macOS binary
- `POST /generate/stager` — Generate stager
- `GET /api/generate/profiles` — List malleable profiles

### Dashboard
- `GET /` — Dashboard page
- `GET /api/dashboard/activity-heatmap` — Activity heatmap
- `GET /api/dashboard/os-distribution` — OS distribution
- `GET /api/dashboard/task-status` — Task status breakdown
- `GET /api/dashboard/listener-traffic` — Listener traffic

### Audit
- `GET /audit/logs` — List audit logs

### Workflows
- `GET /workflows` — List workflows
- `GET /workflows/:id` — Get workflow detail
- `POST /workflows` — Create workflow
- `PUT /workflows/:id` — Update workflow
- `DELETE /workflows/:id` — Delete workflow
- `POST /workflows/:id/toggle` — Toggle workflow enabled/disabled
- `POST /workflows/:id/execute` — Execute workflow

### Plugins
- `GET /api/plugins` — List plugins
- `GET /api/plugins/:id` — Get plugin detail
- `POST /api/plugins` — Create plugin
- `POST /api/plugins/:id/toggle` — Toggle plugin
- `POST /api/plugins/:id/execute` — Execute plugin

### External C2
- `GET /extc2/channels` — List active External C2 channels
- `POST /extc2/discord` — Configure Discord relay
- `POST /extc2/slack` — Configure Slack relay

### Health
- `GET /health` — Health check (no auth)
- `GET /ready` — Readiness check (no auth)

## Python Client Example

```python
from forgec2 import ForgeC2Client

client = ForgeC2Client("http://localhost:8000", "admin", "admin")

# List agents
agents = client.agents.list()
for agent in agents:
    print(f"{agent['hostname']} ({agent['status']})")

# Execute command
task = client.agents.shell(agent_id, "whoami")
print(task['result'])

# List listeners
listeners = client.listeners.list()

# Generate payload
client.generate.exe(
    c2_url="https://example.com",
    interval=30,
    jitter=20
)

# List credentials
creds = client.credentials.list()

# List workflows
workflows = client.workflows.list()

# Execute a workflow
client.workflows.execute(workflow_id)
```

## Rate Limits
- Login: 5 attempts per minute
- API: 100 requests per minute per user
- Agent beacon: 30 requests per minute per agent
