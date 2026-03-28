# YAML Workflows

## Motivation

Directory-based workflows require one file per state — a natural fit for large
workflows, but heavyweight for small ones. A three-state workflow needs a
directory, three `.md` files, and possibly a few `.sh` scripts. YAML workflows
let you define the entire workflow in a single file:

```yaml
states:
  1_START:
    prompt: |
      Greet the user, then transition.
    allowed_transitions:
      - tag: goto
        target: DONE.md
  DONE:
    prompt: |
      Say goodbye.
    allowed_transitions:
      - tag: result
```

Any CLI command that accepts a directory or ZIP scope also accepts a `.yaml` or
`.yml` file.

## Schema

A YAML workflow file has a single required top-level key, `states`, whose value
is a mapping of state names to state definitions:

```yaml
states:
  STATE_NAME:
    # ... state definition (see below)
```

**Constraints:**

- `states` must be present, must be a mapping, and must contain at least one
  entry.
- State names must be non-empty strings and must not contain path separators
  (`/` or `\`).
- Duplicate state names are not allowed.

### Markdown states

A markdown state has a `prompt` key and optional policy fields:

```yaml
states:
  REVIEW:
    prompt: |
      Review the code for correctness.
      Focus on edge cases and error handling.
    allowed_transitions:
      - tag: goto
        target: FIX.md
      - tag: result
    model: sonnet
    effort: high
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `prompt` | string | yes | The prompt text sent to the model (supports `{{result}}` and other template variables — see [authoring-guide.md](authoring-guide.md)) |
| `allowed_transitions` | list | no | Constrains which transitions the model may use |
| `model` | string | no | Model override (`opus`, `sonnet`, `haiku`) |
| `effort` | string | no | Extended thinking level (`low`, `medium`, `high`) |

Each entry in `allowed_transitions` is a mapping with a `tag` key and optional
attributes like `target`, `payload`, `return`, etc. — the same attributes used
in directory-based frontmatter. Targets reference virtual filenames (see
[Virtual files](#virtual-files)).

### Script states

A script state has one or more platform keys (`sh`, `ps1`, `bat`) and no
`prompt`:

```yaml
states:
  BUILD:
    sh: |
      #!/usr/bin/env bash
      set -euo pipefail
      make build 2>&1
    bat: |
      @echo off
      nmake build 2>&1
```

At least one platform key must be present:

| Field | Type | Description |
|-------|------|-------------|
| `sh` | string | Shell script (Unix) |
| `ps1` | string | PowerShell script (Windows) |
| `bat` | string | Batch script (Windows) |

**Restrictions:** Script states must not have `allowed_transitions`, `model`,
or `effort` — these policy fields apply only to markdown states.

### Mutual exclusivity

Each state must be exactly one type — markdown or script. A state with both
`prompt` and a script key (e.g. `sh`) is a validation error. A state with
neither is also an error.

## Virtual files

YAML workflows present states as virtual files so the rest of the system
(orchestrator, lint, diagram) can treat them identically to directory-based
workflows:

| State type | Virtual filename(s) |
|------------|-------------------|
| Markdown | `STATE_NAME.md` |
| Script (sh only) | `STATE_NAME.sh` |
| Script (ps1 only) | `STATE_NAME.ps1` |
| Script (bat only) | `STATE_NAME.bat` |
| Script (multiple) | One file per platform, in order: `.sh`, `.ps1`, `.bat` |

When a markdown state is read, its policy fields are synthesized into YAML
frontmatter — the same format used in directory-based `.md` files. A state with
no policy fields produces bare prompt text with no frontmatter block.

## CLI usage

### Start a workflow

```bash
# Entry point resolved automatically (looks for 1_START or START)
raymond workflow.yaml

# Start at a specific state
raymond workflow.yaml/REVIEW

# With flags
raymond workflow.yaml/REVIEW -m sonnet --input "review this PR"
```

The `workflow.yaml/STATE` syntax is not a filesystem path — the CLI splits on
the YAML extension to extract the scope and initial state.

### Resume

```bash
raymond --resume <workflow-id>
```

On resume, the YAML file is re-parsed and re-validated. If the file has been
deleted or become invalid since the workflow was started, the resume fails with
a clear error.

### Lint

```bash
raymond lint workflow.yaml
raymond lint --json --level error workflow.yaml
```

Runs the same static analysis checks as directory-based workflows (missing
transition targets, unreachable states, etc.).

### Diagram

```bash
raymond diagram workflow.yaml
raymond diagram --html --output diagram.html workflow.yaml
```

Generates a Mermaid flowchart or interactive HTML diagram from the workflow
states and transitions.

## Entry point resolution

When no initial state is specified (`raymond workflow.yaml`), the entry point
is resolved using the same rules as directory-based workflows:

1. Look for a state named `1_START` (any extension)
2. Fall back to a state named `START`
3. If both exist, report an ambiguity error
4. If neither exists, report an error

When an initial state is given (`raymond workflow.yaml/REVIEW`), the bare name
is resolved to its virtual filename (`REVIEW.md` or `REVIEW.sh`, depending on
the state type).

## Errors

The YAML scope produces three error types:

| Error | When |
|-------|------|
| **Parse error** | File cannot be read, or YAML syntax is invalid |
| **Validation error** | YAML is well-formed but violates schema rules (missing `states`, dual-type state, script with policy fields, etc.) |
| **File not found** | A requested virtual filename does not correspond to any state |

When states are defined at the root level without a `states:` wrapper, the
parser detects common state-like keys and suggests adding the wrapper.

## Comparison with other scope types

| | Directory | ZIP | YAML |
|---|-----------|-----|------|
| **One file per state** | yes | yes (inside archive) | no — all states in one file |
| **Best for** | Large workflows, version control | Distribution, immutable snapshots | Small workflows, prototyping |
| **Script support** | `.sh`/`.bat`/`.ps1` files | Same, inside archive | `sh`/`ps1`/`bat` keys in state definition |
| **Policy via frontmatter** | In each `.md` file | Same | `allowed_transitions`, `model`, `effort` keys |
| **CLI syntax** | `raymond dir/` or `raymond dir/FILE.md` | `raymond archive.zip` | `raymond workflow.yaml` or `raymond workflow.yaml/STATE` |

## Complete example

A workflow that reviews code, optionally fixes issues, then reports results:

```yaml
states:
  1_START:
    prompt: |
      You are a code reviewer. Analyze the code provided in {{result}}.
      If you find issues, transition to FIX. Otherwise, transition to REPORT.
    allowed_transitions:
      - tag: goto
        target: FIX.md
      - tag: goto
        target: REPORT.md
    model: sonnet
    effort: high

  FIX:
    prompt: |
      Apply the fixes you identified. When done, transition to REPORT
      with a summary of changes.
    allowed_transitions:
      - tag: goto
        target: REPORT.md

  REPORT:
    prompt: |
      Summarize the review outcome and any changes that were made.
    allowed_transitions:
      - tag: result
```

```bash
raymond workflow.yaml --input "$(cat src/main.go)"
```

Transition targets use virtual filenames — `.md` for markdown states, `.sh` or
`.bat` for script states. For example, a transition to a script state named
`BUILD` would use `target: BUILD.sh` (Unix) or `target: BUILD.bat` (Windows).
