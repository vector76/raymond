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
| `GET` | `/workflows` | List all discovered workflows with metadata (`id`, `name`, `description`, `input`, `default_budget`, `requires_human_input`). |
| `GET` | `/workflows/{id}` | Get details for a specific workflow. |

### Run management

| Method | Endpoint | Description |
|--------|----------|-------------|
| `POST` | `/runs` | Start a workflow run. Accepts JSON body: `{"workflow_id", "input", "budget", "model", "working_directory", "environment"}`. Returns `{"run_id", "status", "started_at"}`. Returns 400 if `input` violates the workflow's `input.mode` (empty for `required`, non-empty for `none`). |
| `GET` | `/runs` | List all runs with status, cost, and timing. |
| `GET` | `/runs/{id}` | Get the status of a single run, including agents, cost, elapsed time, and result. |
| `GET` | `/runs/{id}/output` | Server-Sent Events stream of agent output, state transitions, and other run events. |
| `POST` | `/runs/{id}/cancel` | Cancel a running workflow. Returns `{"run_id", "status": "cancelled"}`. |

### Human-in-the-loop (requires `<await>`)

| Method | Endpoint | Description |
|--------|----------|-------------|
| `GET` | `/runs/{id}/pending-inputs` | List pending human input requests for a run. Each entry includes `run_id`, `input_id`, `prompt`, `created_at`, optional `timeout_at`, and (for file-bearing awaits) optional `file_affordance` and `staged_files` blocks. |
| `POST` | `/runs/{id}/inputs/{input_id}` | Deliver a human response. Accepts either `application/json` (text only) or `multipart/form-data` (text plus files). Returns `{"run_id", "input_id", "status": "resumed"}`. |
| `GET` | `/runs/{id}/inputs/{input_id}/files` | List files associated with an input — display files staged at await entry plus any uploaded files once the input has been delivered. |
| `GET` | `/runs/{id}/inputs/{input_id}/files/{path}` | Serve the named file from the input's subdirectory. Optional `?disposition=inline` is honored only for vetted MIME types (see below). |

#### File-bearing awaits

When an `<await>` declares file affordances (see
[workflow-protocol.md](workflow-protocol.md) for the attribute syntax), the
endpoints above carry the additional shape described here. The on-disk layout
is `<task folder>/inputs/<input_id>/`, populated by the runtime at await entry
(display files) and on submission (uploads).

**Pending-inputs extension.** Each entry may include:

```json
{
  "run_id": "...",
  "input_id": "...",
  "prompt": "...",
  "created_at": "...",
  "timeout_at": "...",
  "file_affordance": {
    "mode": "slot" | "bucket" | "display_only",
    "slots": [{"name": "resume.pdf", "mime": ["application/pdf"]}],
    "bucket": {"max_count": 5, "max_size_per_file": 10485760, "max_total_size": 52428800, "mime": ["image/png"]},
    "display_files": [{"source_path": "out/report.pdf", "display_name": "Final Report"}]
  },
  "staged_files": [
    {"name": "Final Report", "size": 12345, "content_type": "application/pdf", "source": "display"}
  ]
}
```

`file_affordance` and `staged_files` are omitted for text-only awaits.

**Listing endpoint.** `GET /runs/{id}/inputs/{input_id}/files` returns an array
of file metadata for the input — staged display files plus uploaded files
(after delivery). Pending inputs are sourced from the pending registry; once
delivered, the catalog comes from the run state's resolved-input history.

```json
[
  {"name": "Final Report", "size": 12345, "content_type": "application/pdf", "source": "display"},
  {"name": "scan.png",     "size": 67890, "content_type": "image/png",       "source": "upload"}
]
```

`source` is either `"display"` (staged from the workflow's task folder) or
`"upload"` (provided by the user).

**Content endpoint.** `GET /runs/{id}/inputs/{input_id}/files/{path}` serves
the named file. The `path` segment is resolved strictly within the input's
subdirectory; traversal attempts (`..`, absolute paths, symlinks escaping the
subdirectory) return 400.

The recorded content type (sniffed once at upload or staging time via Go's
`http.DetectContentType`) is authoritative — the server does not re-sniff at
view time. Every file response carries:

- `Content-Type: <recorded type>` (or `application/octet-stream` if unknown)
- `Content-Disposition: attachment; filename="<name>"` by default
- `X-Content-Type-Options: nosniff`
- `Content-Security-Policy: default-src 'none'; sandbox`

Passing `?disposition=inline` switches to `inline` **only** when the recorded
content type is on the inline allowlist. The allowlist is server-defined and
not author-controllable:

- `image/png`
- `image/jpeg`
- `image/gif`
- `image/webp`
- `application/pdf`

Anything outside the allowlist is served as `attachment` regardless of the
query parameter, so script-bearing types such as `text/html` and
`image/svg+xml` are never rendered inline by the server.

**Multipart upload contract.** The deliver endpoint dispatches on
`Content-Type`:

- `application/json` — text-only, current behavior preserved (`{"response": "..."}`).
- `multipart/form-data` — text plus file parts.

Multipart requests use these field conventions:

- `response` (text field) — the human-facing text response. Optional if the
  await is upload-only.
- File parts:
  - **Slot mode**: each file part's form field name must match a declared slot
    name (e.g. `resume.pdf`). Every declared slot must appear exactly once;
    unexpected names or duplicates are rejected.
  - **Bucket mode**: form field names are not significant; the saved filename
    is taken from the part's `filename=` attribute. Validated against
    `upload_max_count`, `upload_max_size`, `upload_max_total_size`, and
    `upload_mime`.

Filenames are normalized before persistence: path separators, null bytes,
control characters, leading dots, and platform-reserved names are rejected
with a structured 4xx (the server does not silently rewrite a name). Within a
single submission, two parts saved under the same effective filename are a
`409 Conflict`; an upload that would overwrite a staged display file is also a
`409`. Submissions are atomic — files are buffered into a per-submission
staging directory under `inputs/<input_id>/.staging-*/` and renamed into place
only after every part validates; partial failures leave the input still
pending and the input subdirectory unmodified by the failed attempt.

Validation errors return a structured body naming the failed constraint, for
the UI to highlight the offending field:

```json
{"error": "...", "constraint": "max_file_size"}
```

Constraint values include `slot_missing`, `slot_extra`, `max_count`,
`max_file_size`, `max_total_size`, `mime_not_allowed`, `filename`,
`duplicate_filename`, `collision_with_staged`, `multipart`, and
`input_not_pending`.

#### Server-wide upload caps

The daemon applies size and count caps in this precedence order (mirroring
`resolveBudget`'s ladder):

1. Per-await override — only bucket-mode `<await>` may raise or lower its own
   caps via `upload_max_size`, `upload_max_total_size`, `upload_max_count`.
2. Server-wide config — set by the `serve` command via
   `Server.SetDefaultUploadCaps(perFile, total, count)` after loading
   `.raymond/config.toml` and merging CLI flags. Zero values mean "unset" and
   fall through.
3. Built-in fallbacks — 10 MiB per file, 100 MiB total, 10 files per
   submission.

The total cap is a hard limit on the request body via
`http.MaxBytesReader` — clients cannot stream past it even if per-file
accounting would have rejected the body later.

#### MCP degradation rules

The MCP transport has no native channel for binary file exchange in
`elicitation/create`. The daemon degrades file-bearing awaits as follows:

- **Text-only awaits** — delivered via elicitation as before.
- **Display-only awaits** — delivered via elicitation; the prompt is augmented
  with absolute URLs (under the configured `base_url`) for each staged file so
  an MCP client can fetch them out of band via the file content endpoint. If
  no base URL is configured, the URLs are omitted.
- **Slot- or bucket-mode awaits** (any upload affordance) — **not delivered
  via MCP**. The await stays pending and a warning is logged; the user must
  complete it via the HTTP UI.

Workflow authors targeting both transports should design awaits that are
either text-only or text-plus-display, and avoid making upload affordances
mandatory for progress.

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
`input` (an object with `mode`, `label`, and `description`), `default_budget`,
and `requires_human_input`.

**`raymond_run`** — Launch a workflow run.

Parameters: `workflow_id` (required), `input`, `budget`, `model`,
`working_directory`, `environment` (all optional). Returns `{run_id, status}`.
Rejects workflows requiring human input if the client lacks elicitation
support. Also rejects calls that violate the workflow's `input.mode` (empty
`input` when `mode: required`, or non-empty `input` when `mode: none`).

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
