# Raymond Daemon Server

The `raymond serve` command starts a long-running daemon that discovers
workflows from configured directories and exposes them to clients via an HTTP
API and/or MCP (Model Context Protocol) tool interface. The daemon manages
workflow runs, handles human-in-the-loop `<ask>` input delivery, and provides
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
| `--launch` | string slice | (none) | Workflow id to dispatch automatically once transports are up. May be repeated. The id must be discoverable via `--root`. |
| `--dangerously-skip-permissions` | bool | true | Server-wide skip-permissions value applied to every run launched via the HTTP API, MCP, or `--launch`. Pass `--dangerously-skip-permissions=false` to require permissions instead. Mirrors the `[raymond].dangerously_skip_permissions` config key with the same precedence (CLI > config > default). |
| `--clean` | bool | false | Archive every non-terminal state file in the serve pool (`.raymond/serve-state/`) into `serve-state/abandoned/<timestamp>/` before recovery runs. Only the serve pool is touched — CLI runs in `.raymond/state/` are untouched. See [Operational flags](#operational-flags). |

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

### Auto-launching workflows on startup

Pass one or more `--launch <workflow_id>` flags to dispatch workflows
automatically once the daemon's transports are ready:

```
raymond serve --root ./workflows --launch nightly-report --launch health-check
```

Each `--launch` is equivalent to a `POST /runs` with body
`{"workflow_id": "<id>"}` and no other fields — input, budget, model,
working directory, and environment are all left to the workflow's defaults
and the server-wide configuration.

Operational properties:

1. **Per-invocation only** — there is no cron schedule, retry, or
   persistence across restarts. Each `serve` invocation launches exactly
   the ids it was given on the command line.
2. **Failures do not abort startup** — if a launch fails (unknown id,
   `input.mode: required` workflow, etc.), the error is logged alongside
   other startup status messages and the daemon continues serving. Other
   launches and the HTTP/MCP transports are unaffected.
3. **Order of run creation is not guaranteed** — launches are dispatched
   concurrently. Do not depend on the flag order matching run start order.

The typical use case is capturing `--launch` flags in a systemd unit or
Docker entrypoint so that operationally-required workflows (health checks,
periodic reports driven by an external scheduler that restarts the daemon,
etc.) start automatically with the server.

## Directory layout

The daemon and the CLI use **disjoint** on-disk pools. Each pool has exactly
one owner runtime, and neither runtime reads the other's pool during
recovery. See [serve-run-pool.md](serve-run-pool.md) for the design.

| Path | Owner | Purpose |
|------|-------|---------|
| `<project>/.raymond/state/` | `ray <workflow>` (CLI) | Persistent state for runs launched from the command line. The daemon never reads this directory. |
| `<project>/.raymond/serve-state/` | `ray serve` (daemon) | Persistent state for runs launched through the HTTP API, MCP, or `--launch`. The CLI never writes here. |
| `<project>/.raymond/serve-state/abandoned/<timestamp>/` | `ray serve --clean` | Forensic archive of non-terminal serve-state files moved aside by `--clean`. Never auto-resumed; left on disk so an operator can inspect or hand-recover. |
| `<project>/.raymond/pending_inputs.jsonl` | `ray serve` (daemon) | Pending-input registry for `<ask>` records. Lives at the `.raymond/` root, not under the serve pool — one registry per project, intentionally not per pool (see the comment preceding `NewPendingRegistry` in `internal/cli/serve.go`). |

The CLI pool and the serve pool are siblings, not parent-and-child. The
daemon resolves its pool through the serve-state path and never falls back
to the CLI pool, so a CLI run left non-terminal by an operator's Ctrl-C will
not appear in the daemon's view. The dependent operational behaviors —
[`--clean`](#operational-flags), [diagnostic listings](#diagnostics), and
[run recovery](#run-recovery) — all derive from this layout.

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
three scope types. The daemon does not perform dynamic ask-scanning during
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

### Human-in-the-loop (requires `<ask>`)

| Method | Endpoint | Description |
|--------|----------|-------------|
| `GET` | `/runs/{id}/pending-asks` | List pending human input requests for a run. Each entry includes `run_id`, `ask_id`, `prompt`, `created_at`, optional `timeout_at`, and (for file-bearing asks) optional `file_affordance` and `staged_files` blocks. |
| `POST` | `/runs/{id}/asks/{ask_id}` | Deliver a human response. Accepts either `application/json` (text only) or `multipart/form-data` (text plus files). Returns `{"run_id", "ask_id", "status": "resumed"}`. |
| `GET` | `/runs/{id}/asks/{ask_id}/files` | List files associated with an input — display files staged at ask entry plus any uploaded files once the input has been delivered. |
| `GET` | `/runs/{id}/asks/{ask_id}/files/{path}` | Serve the named file from the input's subdirectory. Optional `?disposition=inline` is honored only for vetted MIME types (see below). |

#### File-bearing asks

When an `<ask>` declares file affordances (see
[workflow-protocol.md](workflow-protocol.md) for the attribute syntax), the
endpoints above carry the additional shape described here. The on-disk layout
is `<task folder>/asks/<ask_id>/`, populated by the runtime at ask entry
(display files) and on submission (uploads).

**Pending-inputs extension.** Each entry may include:

```json
{
  "run_id": "...",
  "ask_id": "...",
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

`file_affordance` and `staged_files` are omitted for text-only asks.

**Listing endpoint.** `GET /runs/{id}/asks/{ask_id}/files` returns an array
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

**Content endpoint.** `GET /runs/{id}/asks/{ask_id}/files/{path}` serves
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
  ask is upload-only.
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
staging directory under `asks/<ask_id>/.staging-*/` and renamed into place
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

1. Per-ask override — only bucket-mode `<ask>` may raise or lower its own
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
`elicitation/create`. The daemon degrades file-bearing asks as follows:

- **Text-only asks** — delivered via elicitation as before.
- **Display-only asks** — delivered via elicitation; the prompt is augmented
  with absolute URLs (under the configured `base_url`) for each staged file so
  an MCP client can fetch them out of band via the file content endpoint. If
  no base URL is configured, the URLs are omitted.
- **Slot- or bucket-mode asks** (any upload affordance) — **not delivered
  via MCP**. The ask stays pending and a warning is logged; the user must
  complete it via the HTTP UI.

Workflow authors targeting both transports should design asks that are
either text-only or text-plus-display, and avoid making upload affordances
mandatory for progress.

### SSE output stream

The `GET /runs/{id}/output` endpoint returns a Server-Sent Events stream. Each
event is a JSON envelope with a `type` field indicating the event kind (e.g.,
`state_started`, `state_completed`, `agent_spawned`, `agent_ask_started`,
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
result, cost_usd}`. If the MCP client supports elicitation, any `<ask>`
prompts encountered during the run are delivered to the client via
`elicitation/create` requests and responses are injected automatically.

**`raymond_cancel`** — Cancel a running workflow.

Parameters: `run_id`. Returns `{run_id, status: "cancelled"}`.

**`raymond_list_pending_asks`** — List all pending human input requests.

Returns an array with `run_id`, `ask_id`, `workflow_id`, `prompt`,
`created_at`, and `timeout_at`.

**`raymond_answer_ask`** — Deliver a response to a pending ask.

Parameters: `ask_id`, `response`. Returns `{run_id, ask_id, status:
"resumed"}`.

### MCP Elicitation

When a connected MCP client declares elicitation capability (in the
`initialize` handshake), the daemon can deliver `<ask>` prompts directly to
the client via `elicitation/create` requests. This allows MCP callers to handle
human-in-the-loop workflows transparently: the caller invokes `raymond_run`
then `raymond_await`, and any human input requests are surfaced as elicitation
prompts without additional API calls.

If the client does not support elicitation, workflows that require human input
(`requires_human_input: true` or detected via auto-scan) are rejected at launch
time with a specific error message.

## Pending Input Registry

The daemon maintains a durable registry of pending human input requests. When
an agent enters `<ask>` in daemon mode, the orchestrator registers a pending
input record with the prompt text, timeout deadline, and target state.

The registry is backed by a JSONL append-only log file (`pending_inputs.jsonl`)
that lives at the project's `.raymond/` directory root — **not** under the
serve pool. There is exactly one pending registry per project, intentionally
not per pool: an `<ask>`'s identity (run id + ask id) is independent of which
pool the run belongs to, and any future inspector should not have to guess a
pool first. See the comment preceding `NewPendingRegistry` in
`internal/cli/serve.go` for the recorded rationale.

On startup, the log is replayed to reconstruct the current pending input set,
then compacted to a clean file.

Input delivery is atomic: `GetAndRemove` claims a pending input and removes it
from the registry in a single operation, preventing duplicate delivery.

### Asking-state survival across restarts

Pending asks survive a daemon restart and remain answerable through the HTTP
and MCP transports without an explicit per-run resume call. The mechanism is:

1. `pending_inputs.jsonl` is replayed before run recovery, so the in-memory
   pending set is rebuilt first.
2. The serve pool's auto-resume (see [Run Recovery](#run-recovery)) brings
   every non-terminal state file back as an active run, including those whose
   agents are in the `asking` status. The relaunch path wires the same
   `askInputCh` / `AskCallback` that a freshly-launched run uses, so a
   recovered ask receives input through the normal delivery routes —
   `POST /runs/{id}/asks/{ask_id}` over HTTP, or `raymond_answer_ask` over
   MCP. (Auto-pushed `elicitation/create` requests are not re-issued to
   clients that connect after the restart; an MCP client recovers a pending
   ask by calling `raymond_list_pending_asks` and answering it explicitly.)
3. Pending registry entries whose paired serve-pool state file is missing at
   startup are dropped with a log line during the prune pass. This keeps the
   registry and the pool consistent — there is never a pending record naming
   a run the daemon does not know about.

## Timeout Monitor

A background goroutine periodically checks the pending input registry for
expired inputs. When a timeout elapses:

- If `timeout_next` is set: the agent receives an empty response and
  transitions to the timeout state (the `{{input}}` variable is empty in the
  timeout state's prompt).
- If `timeout_next` is not set: the agent fails with an error.

CLI pause mode (`--on-ask=pause`) does not have built-in timeout monitoring —
the process is not running between resume cycles.

## Operational flags

### `--clean`

`ray serve --clean` archives every non-terminal state file in the serve pool
before recovery runs. The behavior is:

- Each non-terminal `*.json` state file in `<project>/.raymond/serve-state/`
  is **moved** (not deleted) into a fresh
  `<project>/.raymond/serve-state/abandoned/<timestamp>/` subdirectory. The
  timestamp is UTC with nanosecond precision so back-to-back `--clean`
  invocations (e.g. a restart loop) cannot collide on the directory name.
- Terminal state files (already-completed runs) are left in place.
- After the move, the dangling-record drop policy fires on the next pending
  registry prune: any `pending_inputs.jsonl` entry that named one of the
  archived runs is dropped with a log line, since its paired state file is
  no longer present in the pool. This is the same rule that fires when a
  serve-state file goes missing for any other reason at startup.
- The daemon then recovers what remains, which is by construction nothing
  non-terminal. It starts with an empty active-run set.

Safety story: `--clean` only acts on the serve pool. CLI runs in
`.raymond/state/` are never touched, because the daemon does not read or
write that directory. Listing tooling avoids the `abandoned/` archive by
construction — `ray serve list` reads only the top level of the serve pool
(see [Diagnostics](#diagnostics)).

Use `--clean` when an operator wants the daemon to come up cold, abandoning
in-flight serve work without losing the on-disk evidence of what was
running.

## Diagnostics

Two read-only listings are available, each scoped to a single pool:

| Command | Pool | Notes |
|---------|------|-------|
| `ray --list` | CLI (`.raymond/state/`) | Pre-existing CLI listing; unchanged by the disjoint-pool work. |
| `ray serve list` | Serve (`.raymond/serve-state/`) | Daemon-free inspector; works without `ray serve` running. Reads only the top level of the serve pool, so the `abandoned/<ts>/` archives created by `--clean` are not enumerated. |

Output is one workflow id per line, sorted alphanumerically. The two views
are intentionally **not** merged into a single command — an operator who
wants both unions them explicitly. Keeping them separate preserves the
property that every id in a given listing was created by exactly one
runtime, which is the same disjoint-pool guarantee recovery relies on.

`ray --status <id>` is the documented exception: as a read-only diagnostic
it consults both pools. The CLI pool is checked first; on a not-found, the
serve pool is checked. When the same id is present in both pools (rare,
since each pool's id namespace is independent), the CLI copy wins. The
not-found error stays generic so an operator probing for an id cannot
learn pool layout from the response.

## Run Recovery

On startup, the daemon scans **only** the serve pool
(`<project>/.raymond/serve-state/`) for persisted workflow state files. The
CLI pool at `.raymond/state/` is never consulted, by construction: the
daemon resolves its state directory through the serve-pool path and the
recovery scan is anchored there.

The serve pool is **curated** — every entry in it was created by an earlier
`ray serve` invocation in this project — so the daemon **actively
auto-resumes** every non-terminal entry on startup. There is no
"recovered but inactive" intermediate state for serve-pool runs. If a state
file is present and not in a terminal status, the daemon brings the run
back to life under its own orchestrator. Recovered runs are visible in the
HTTP API and web UI immediately, and continue running without operator
intervention.

`asking`-state runs are auto-resumed the same way: the relaunch wires the
normal `askInputCh` / `AskCallback`, and the pre-replayed
`pending_inputs.jsonl` makes the recovered ask immediately answerable
through `POST /runs/{id}/asks/{ask_id}` — no per-run resume call is
required. See [Asking-state survival across restarts](#asking-state-survival-across-restarts)
for the full mechanism.

### Dangling pending-registry records

When the pending registry is replayed, an entry whose paired serve-pool
state file is missing is **dropped with a log line**. This can happen
because:

- The state file was archived by `ray serve --clean` (intentional).
- The state file was removed out-of-band by an operator (rare).
- The run was deleted before its registry compaction completed (corner
  case).

In all three cases the policy is the same: the registry is reconciled to
match the on-disk pool, so the in-memory and on-disk views are consistent
by the time the first orchestrator goroutine starts. There is never a
pending record naming a run the daemon does not know about.

### Nested launches go to the CLI pool

A workflow that shells out to `ray <workflow_id>` from inside a serve-pool
run produces a state file in `.raymond/state/`, **not** in
`.raymond/serve-state/`. The `ray` binary is not treated specially when
invoked from another raymond process: it is a normal shell command that
happens to launch a workflow, and its state lands in the CLI pool wherever
the CLI's resolution rule says.

Consequences for workflow authors:

- The nested run is **detached from the daemon's lifecycle**. It does not
  appear in the daemon's run list, does not stream events through the
  parent run's SSE channel, and is not aborted when the parent run is
  cancelled.
- `ray serve --clean` does **not** touch the nested run's state file,
  because `--clean` only scopes to the serve pool.
- The nested run has its own SIGINT handling, its own budget, and its own
  termination semantics.

If the intent is "this nested work is part of my serve run" — same
orchestrator, same budget, same lifecycle, same daemon view — the right
primitive is **`<fork>`** or **`<fork-workflow>`**. Both run in-process
under the same orchestrator and produce no separate state file. Shelling
out to `ray` is the explicit choice to detach.

See [workflow-protocol.md](workflow-protocol.md) for `<fork>` and
[cross-workflow-design.md](cross-workflow-design.md) for `<fork-workflow>`;
the guidance above is the daemon-side framing of the same trade-off.
