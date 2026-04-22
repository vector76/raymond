# Raymond Daemon Server

The `raymond serve` command starts a long-running daemon that discovers
workflows from configured directories and exposes them to clients via an HTTP
API and/or MCP (Model Context Protocol) tool interface. The daemon manages
workflow runs, handles human-in-the-loop `<await>` input delivery, and provides
a minimal web UI for monitoring.

## Usage

```
raymond serve --root <dir> [--root <dir2> ...] [flags]
```

### Flags

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--root` | string slice | (required) | One or more directories to scan for workflows. Each root is scanned for subdirectories, zip archives, and YAML workflow files carrying manifest metadata. |
| `--port` | int | 8080 | Port for the HTTP API server. |
| `--mcp` | bool | false | Enable the MCP transport interface. |
| `--no-http` | bool | false | Disable the HTTP server entirely. Requires `--mcp` — at least one transport must be active. |
| `--workdir` | string | (none) | Default working directory for workflow runs. |

### Examples

Start the daemon scanning a single root:

```
raymond serve --root ./workflows
```

Scan multiple roots with a custom port:

```
raymond serve --root ./workflows --root /opt/shared-workflows --port 9090
```

MCP-only mode (no HTTP server):

```
raymond serve --root ./workflows --mcp --no-http
```

## Workflow Registry

The daemon's workflow registry discovers and indexes available workflows at
startup (and on rescan). The discovery process:

1. Iterates over each configured `--root` directory.
2. For each immediate child entry:
   - **Directories**: checks for a `workflow.yaml` manifest file inside the directory.
   - **Zip archives** (`.zip` files): checks for a `workflow.yaml` entry inside the archive.
   - **YAML files** (`.yaml` / `.yml`): checks for embedded manifest metadata (top-level `id` alongside `states`). See [yaml-workflows.md](yaml-workflows.md#manifest-metadata-for-daemon-discovery).
3. Parses the manifest and extracts metadata (ID, name, description, input schema, default budget, human-input requirements).
4. Entries without a valid manifest source are silently skipped. For YAML files, this means the file either lacks `states`, lacks `id`, or fails to parse.

The registry supports rescanning at runtime to pick up newly added or removed
workflows without restarting the daemon.

### Manifest requirements

For a workflow to be discoverable by the daemon, it must provide manifest
metadata with at least an `id` field. There are three valid manifest sources:

- A `workflow.yaml` file inside a directory scope.
- A `workflow.yaml` entry inside a zip archive.
- A YAML workflow file (`.yaml` / `.yml`) with top-level manifest fields
  embedded alongside `states`. See
  [yaml-workflows.md](yaml-workflows.md#manifest-metadata-for-daemon-discovery)
  for the recognized fields.

See the `internal/manifest` package for the full schema and
[skill-packaging.md](skill-packaging.md) for the packaging conventions.

`requires_human_input: auto` resolves to `false` at discovery time across all
three scope types. The daemon does not perform dynamic await-scanning during
indexing; workflows that need `auto` promoted to `true` must set the field
explicitly.

## HTTP API

The HTTP API provides a RESTful interface for workflow discovery, run
management, streaming output, and human-in-the-loop input delivery.

### Workflow discovery

| Method | Endpoint | Description |
|--------|----------|-------------|
| `GET` | `/workflows` | List all discovered workflows with metadata (`id`, `name`, `description`, `input_schema`, `default_budget`, `requires_human_input`). |
| `GET` | `/workflows/{id}` | Get details for a specific workflow. |

### Run management

| Method | Endpoint | Description |
|--------|----------|-------------|
| `POST` | `/runs` | Start a workflow run. Accepts JSON body: `{"workflow_id", "input", "budget", "model", "working_directory", "environment"}`. Returns `{"run_id", "status", "started_at"}`. |
| `GET` | `/runs` | List all runs with status, cost, and timing. |
| `GET` | `/runs/{id}` | Get the status of a single run, including agents, cost, elapsed time, and result. |
| `GET` | `/runs/{id}/output` | Server-Sent Events stream of agent output, state transitions, and other run events. |
| `POST` | `/runs/{id}/cancel` | Cancel a running workflow. Returns `{"run_id", "status": "cancelled"}`. |

### Human-in-the-loop (requires `<await>`)

| Method | Endpoint | Description |
|--------|----------|-------------|
| `GET` | `/runs/{id}/pending-inputs` | List pending human input requests for a run. Each entry includes `run_id`, `input_id`, `prompt`, `created_at`, and optional `timeout_at`. |
| `POST` | `/runs/{id}/inputs/{input_id}` | Deliver a human response. Accepts JSON body: `{"response": "..."}`. Returns `{"run_id", "input_id", "status": "resumed"}`. |

### SSE output stream

The `GET /runs/{id}/output` endpoint returns a Server-Sent Events stream. Each
event is a JSON envelope with a `type` field indicating the event kind (e.g.,
`state_started`, `state_completed`, `agent_spawned`, `agent_await_started`,
`claude_stream_output`, `workflow_completed`). The stream closes when the run
reaches a terminal state.

### Web UI

The daemon serves a minimal web UI at the root path (`/`). The UI provides:

- **Active runs** with current state, cost, and elapsed time
- **Run history** of completed, failed, and cancelled runs
- **Pending human inputs** with full prompt text and a response form
- **Live output** via SSE streaming when a run is selected

The UI is a static single-page application (HTML, CSS, JavaScript) that calls
the HTTP API endpoints above. No additional backend is needed.

## MCP Tool Interface

When `--mcp` is enabled, the daemon exposes a fixed set of MCP tools for
workflow discovery, run management, and input delivery. The MCP transport
operates over stdio using JSON-RPC 2.0, compatible with standard MCP client
implementations.

### Tools

**`raymond_list_workflows`** — List all discovered workflows.

Returns an array of workflow objects with `id`, `name`, `description`,
`input_schema`, `default_budget`, and `requires_human_input`.

**`raymond_run`** — Launch a workflow run.

Parameters: `workflow_id` (required), `input`, `budget`, `model`,
`working_directory`, `environment` (all optional). Returns `{run_id, status}`.
Rejects workflows requiring human input if the client lacks elicitation
support.

**`raymond_status`** — Get current status of a run.

Parameters: `run_id`. Returns `{run_id, status, current_state, agents,
cost_usd, elapsed_seconds}`.

**`raymond_await`** — Block until a run completes.

Parameters: `run_id`, `timeout_seconds` (optional). Returns `{run_id, status,
result, cost_usd}`. If the MCP client supports elicitation, any `<await>`
prompts encountered during the run are delivered to the client via
`elicitation/create` requests and responses are injected automatically.

**`raymond_cancel`** — Cancel a running workflow.

Parameters: `run_id`. Returns `{run_id, status: "cancelled"}`.

**`raymond_list_pending_inputs`** — List all pending human input requests.

Returns an array with `run_id`, `input_id`, `workflow_id`, `prompt`,
`created_at`, and `timeout_at`.

**`raymond_provide_input`** — Deliver a response to a pending await.

Parameters: `input_id`, `response`. Returns `{run_id, input_id, status:
"resumed"}`.

### MCP Elicitation

When a connected MCP client declares elicitation capability (in the
`initialize` handshake), the daemon can deliver `<await>` prompts directly to
the client via `elicitation/create` requests. This allows MCP callers to handle
human-in-the-loop workflows transparently: the caller invokes `raymond_run`
then `raymond_await`, and any human input requests are surfaced as elicitation
prompts without additional API calls.

If the client does not support elicitation, workflows that require human input
(`requires_human_input: true` or detected via auto-scan) are rejected at launch
time with a specific error message.

## Pending Input Registry

The daemon maintains a durable registry of pending human input requests. When
an agent enters `<await>` in daemon mode, the orchestrator registers a pending
input record with the prompt text, timeout deadline, and target state.

The registry is backed by a JSONL append-only log file (`pending_inputs.jsonl`)
in the daemon's state directory. On startup, the log is replayed to reconstruct
the current pending input set, then compacted to a clean file.

Input delivery is atomic: `GetAndRemove` claims a pending input and removes it
from the registry in a single operation, preventing duplicate delivery.

## Timeout Monitor

A background goroutine periodically checks the pending input registry for
expired inputs. When a timeout elapses:

- If `timeout_next` is set: the agent receives an empty response and
  transitions to the timeout state (the `{{result}}` variable is empty in the
  timeout state's prompt).
- If `timeout_next` is not set: the agent fails with an error.

CLI pause mode (`--on-await=pause`) does not have built-in timeout monitoring —
the process is not running between resume cycles.

## Run Recovery

On startup, the daemon scans the state directory for persisted workflow state
files. Previously running workflows are recovered in their last known state:
agents in the `awaiting` status are surfaced as `awaiting_input` runs, and
other interrupted runs are marked as `failed`. Recovered runs are visible in
the HTTP API and web UI immediately.
