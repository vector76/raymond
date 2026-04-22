# Skill Packaging

A **skill package** is a self-contained directory (or zip archive) that wraps a
Raymond workflow so that an outer orchestrator — Claude Code, another Raymond
instance, or a custom script — can invoke it as a black box. The package
advertises its interface through a contract file and provides a single entry
point script that handles both initial runs and resume cycles.

## Three-File Convention

Every skill package contains at least three files:

| File | Purpose |
|------|---------|
| `SKILL.md` | Interface contract — describes inputs, outputs, human-input expectations, and invocation instructions for callers. |
| `run.sh` / `run.bat` | Entry point script — the only thing callers execute directly. Handles both `run` (first invocation) and `resume` (subsequent input delivery). |
| `workflow.yaml` | Daemon manifest — metadata for discovery by `raymond serve` and other tooling. |

The rest of the directory is a normal Raymond workflow scope: state files
(`.md`, `.sh`), and optionally a `states/` subdirectory when the workflow is
organized that way.

## Entry Point Script (`run.sh`)

The entry point uses `exec` so that Raymond's exit code propagates directly to
the caller — no wrapper shell sits in between to swallow or remap it.

```bash
#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

case "${1:-run}" in
  run)
    exec raymond "$SCRIPT_DIR" \
      --on-await=pause \
      --budget "${BUDGET:-5.00}" \
      ${INPUT:+--input "$INPUT"}
    ;;
  resume)
    exec raymond \
      --resume "$RUN_ID" \
      --input "$INPUT"
    ;;
  *)
    echo "Usage: run.sh [run|resume]" >&2
    exit 1
    ;;
esac
```

**Key points:**

- `exec` replaces the shell process with `raymond`, so the caller sees
  Raymond's exit code directly.
- The `run` case passes `--on-await=pause` so the workflow exits cleanly at
  await points instead of rejecting them.
- The `resume` case requires `RUN_ID` (from the previous exit's JSON output)
  and `INPUT` (the human's response).
- Environment variables (`BUDGET`, `INPUT`, `RUN_ID`) are the recommended way
  for callers to pass parameters — they work identically across platforms and
  avoid shell quoting issues.

## Exit Code Protocol

The entry point script (and Raymond itself) uses three exit codes:

| Exit Code | Meaning | Caller Action |
|-----------|---------|---------------|
| **0** | Workflow completed successfully. | Done — read stdout for final output. |
| **1** | Error (invalid arguments, workflow failure, etc.). | Report failure — do not resume. |
| **2** | Workflow is paused, awaiting human input. | Parse the JSON on stdout, collect the requested input, and resume. |

## Structured JSON Output (Exit Code 2)

When Raymond exits with code 2, it writes a JSON object to stdout describing
the active await point:

```json
{
  "status": "awaiting_input",
  "run_id": "vendor-approval-a1b2c3",
  "workflow": "vendor-approval",
  "awaiting": {
    "input_id": "inp_main_1713750000000000000",
    "agent_id": "main",
    "prompt": "Please approve or reject vendor Acme Corp (budget: $50,000)"
  },
  "pending_count": 0,
  "resume": "raymond --resume vendor-approval-a1b2c3 --input \"[your response]\""
}
```

**Fields:**

| Field | Type | Description |
|-------|------|-------------|
| `status` | string | Always `"awaiting_input"`. |
| `run_id` | string | Workflow run ID — pass this to `--resume`. |
| `workflow` | string | Base name of the workflow scope directory. |
| `awaiting.input_id` | string | Unique identifier for this specific await point. |
| `awaiting.agent_id` | string | Which agent is waiting for input. |
| `awaiting.prompt` | string | Human-facing prompt text from the `<await>` tag in the state file. |
| `pending_count` | int | Number of *additional* agents also awaiting input (not counting the active one). If 2, there are 3 total awaiting agents that need input before the workflow can complete. |
| `resume` | string | Pre-formatted command hint for resuming with input. |

## Resume Loop Protocol

Callers drive the skill through a run/resume loop:

```
┌─────────────┐
│  run.sh run │
└──────┬──────┘
       │
       ▼
   ┌────────┐    exit 0    ┌──────┐
   │ raymond │────────────▶│ done │
   └────┬───┘              └──────┘
        │
        │ exit 2 (JSON on stdout)
        ▼
   ┌─────────────┐
   │ parse JSON   │◀─────────────────────┐
   │ show prompt  │                      │
   │ collect input│                      │
   └──────┬──────┘                      │
          │                              │
          ▼                              │
   ┌──────────────┐   exit 2            │
   │ run.sh resume │───────────────────▶│
   └──────┬───────┘                     │
          │                              │
          │ exit 0                       │
          ▼                              │
     ┌──────┐                            │
     │ done │                            │
     └──────┘                            │
```

**Pseudocode for callers:**

```bash
# Initial run
INPUT="vendor name=Acme Corp" \
  ./run.sh run > output.json
exit_code=$?

# Resume loop
while [ "$exit_code" -eq 2 ]; do
  run_id=$(jq -r '.run_id' output.json)
  prompt=$(jq -r '.awaiting.prompt' output.json)

  echo "Workflow needs input: $prompt"
  read -rp "> " response

  RUN_ID="$run_id" INPUT="$response" \
    ./run.sh resume > output.json
  exit_code=$?
done

if [ "$exit_code" -eq 0 ]; then
  echo "Workflow completed successfully"
else
  echo "Workflow failed (exit code $exit_code)"
fi
```

**Multi-agent awaits:** When `pending_count > 0`, additional agents are also
waiting for input. After delivering input for the active await and receiving
exit code 2 again, the JSON will describe the next awaiting agent. The caller
must loop until either exit code 0 (all inputs delivered, workflow complete) or
exit code 1 (error).

## `SKILL.md` Contract

The `SKILL.md` file is the interface contract that tells callers — human or
AI — how to use the skill. It is not executed by Raymond; it exists purely
for documentation and discovery.

### Conventions

A `SKILL.md` should include:

1. **Summary** — one-line description of what the skill does.
2. **Inputs** — environment variables or `--input` values the skill expects,
   with types and defaults.
3. **Outputs** — what the skill produces on success (files written, stdout
   content, etc.).
4. **Human Input Expectations** — whether the skill will pause for human input,
   what kind of input is requested, and how many await cycles to expect.
5. **Invocation Instructions** — exact commands to run the skill, including
   the resume loop pattern if the skill uses `<await>`.
6. **Exit Codes** — reiterate the standard protocol (0/1/2) plus any
   skill-specific semantics.

### Example

See [examples/vendor-approval/SKILL.md](../examples/vendor-approval/SKILL.md)
for a complete example.

## `workflow.yaml` Manifest

The manifest provides metadata for `raymond serve` and other tooling that
discovers and indexes skills.

```yaml
id: vendor-approval
name: Vendor Approval Workflow
description: Evaluates a vendor and routes through human approval before generating a recommendation report.
input_schema:
  vendor_name: string
  budget_limit: string
default_budget: 5.0
requires_human_input: auto
```

**Key fields for skill packaging:**

| Field | Description |
|-------|-------------|
| `id` | Unique identifier — used as the tool name in MCP and the endpoint name in the HTTP API. |
| `name` | Human-readable display name. |
| `description` | Shown in tool/endpoint documentation. |
| `input_schema` | Parameters the skill accepts — used for API validation and documentation. |
| `default_budget` | Default USD budget when callers don't specify one. |
| `requires_human_input` | `"auto"` (scan states for `<await>` tags), `"true"` (always), or `"false"` (never). Controls how the daemon handles the workflow. |

When `requires_human_input` is `"auto"`, Raymond scans the workflow's state
files for `<await>` transitions in their frontmatter. If any state declares
`{tag: await}` in its `allowed_transitions`, the workflow is marked as
requiring human input. This scan is transitive — it follows `<call-workflow>`
and `<function-workflow>` references into child workflows.

## Directory Layout

The simplest layout puts state files alongside the packaging files:

```
vendor-approval/
  SKILL.md              # Interface contract
  run.sh                # Entry point (Unix)
  run.bat               # Entry point (Windows, optional)
  workflow.yaml         # Daemon manifest
  1_START.md            # State: research the vendor
  2_REVIEW.md           # State: await human approval
  3_REPORT.md           # State: generate final report
```

When the skill has many state files, a `states/` subdirectory keeps the
top-level directory clean:

```
vendor-approval/
  SKILL.md
  run.sh
  workflow.yaml
  states/
    1_START.md
    2_REVIEW.md
    3_REPORT.md
```

With the subdirectory layout, the entry point script must pass the `states/`
path as the scope directory — Raymond resolves state files directly within the
scope it is given, without searching subdirectories:

```bash
exec raymond "$SCRIPT_DIR/states" \
  --on-await=pause ...
```
