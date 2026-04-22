# Embedded Manifest in YAML Workflows

## Problem

The daemon registry (`raymond serve`) discovers workflows by scanning root
directories for subdirectories and zip archives that contain a `workflow.yaml`
manifest. YAML scope files (single-file workflows ending in `.yaml` or `.yml`)
are silently skipped because they are neither directories nor zip archives.

This means a workflow defined as a single YAML file — the format designed for
lightweight, low-ceremony workflows — cannot be discovered or launched through
the daemon, HTTP API, MCP tools, or web UI. The only way to run a YAML
workflow is via direct CLI invocation (`raymond workflow.yaml`).

The root cause is that the manifest and the workflow definition are treated as
mutually exclusive. The manifest parser (`ParseManifestData`) explicitly
rejects any YAML file containing a `states` key, returning `ErrNotManifest`.
The YAML scope parser (`yamlscope.Parse`) only reads the `states` key and
ignores everything else. There is no code path that reads both from the same
file.

## Solution

Allow manifest metadata to be embedded directly in YAML workflow files as
top-level keys alongside `states`. A YAML workflow with an `id` field becomes
self-describing and discoverable by the daemon — no separate manifest file
needed.

```yaml
id: code-review
name: Code Review Pipeline
description: Reviews code and optionally fixes issues
default_budget: 3.0
input_schema:
  file_path: string
requires_human_input: auto

states:
  1_START:
    prompt: |
      Review the code in {{result}} for correctness.
    allowed_transitions:
      - tag: goto
        target: FIX.md
      - tag: goto
        target: REPORT.md

  FIX:
    prompt: |
      Apply the fixes you identified, then transition to REPORT.
    allowed_transitions:
      - tag: goto
        target: REPORT.md

  REPORT:
    prompt: |
      Summarize the review outcome.
    allowed_transitions:
      - tag: result
```

This is the true single-file workflow: definition, metadata, and daemon
discoverability in one place.

## Manifest Fields

The following top-level keys are recognized as manifest metadata when they
appear alongside `states`. All are optional except `id` (required for daemon
discovery). These are the same fields supported by standalone `workflow.yaml`
manifest files:

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `id` | string | (none) | Unique identifier. Required for daemon discovery. Used as the workflow ID in the HTTP API and MCP tools. |
| `name` | string | `""` | Human-readable display name. |
| `description` | string | `""` | Description shown in tool/endpoint documentation. |
| `input_schema` | map[string]string | `nil` | Parameters the workflow accepts. |
| `default_budget` | float64 | `0` | Default USD budget when callers don't specify one. |
| `working_directory` | string | `""` | Default working directory for runs. |
| `environment` | map[string]string | `nil` | Environment variables for runs. Supports `${VAR}` interpolation. |
| `requires_human_input` | string | `"auto"` | `"auto"`, `"true"`, or `"false"`. Controls whether the daemon rejects the workflow from contexts that cannot deliver human input. |

## Behavior

### CLI usage (unchanged)

YAML workflows without manifest fields continue to work exactly as before.
Adding manifest fields to a YAML workflow file has no effect on CLI behavior —
the CLI does not use them. `raymond workflow.yaml` runs the workflow regardless
of whether `id` is present.

### Daemon discovery

The registry scan adds a third branch: after checking for directories and zip
archives, it checks for `.yaml`/`.yml` files. For each YAML file found in a
root directory:

1. Parse the file.
2. If it contains `states` and `id`, index it as a discoverable workflow. The
   `ScopeDir` is the path to the YAML file itself (consistent with how zip
   scopes set `ScopeDir` to the zip file path).
3. If it contains `states` but no `id`, skip it silently — it's a valid YAML
   workflow but not intended for daemon discovery.
4. If it does not contain `states`, skip it — it might be a standalone manifest
   or an unrelated YAML file.

### Launching runs

The run manager already handles YAML scopes correctly via
`specifier.ResolveEntryPoint`, which dispatches on scope type. No changes are
needed to run launching, state creation, or orchestration — a YAML scope's
`ScopeDir` is the file path, and the existing machinery handles entry point
resolution, virtual file access, and state persistence for YAML scopes.

### `requires_human_input` resolution

The `auto` mode already works for YAML scopes. The `scanYamlScopeForHumanInput`
function in `manifest/resolve.go` parses the YAML file and scans its states for
`await` transitions. The only new requirement is that the daemon registry can
invoke this resolution when indexing a YAML workflow with
`requires_human_input: auto` (or when the field is absent, since `auto` is the
default).

Currently the daemon registry sets `requires_human_input` to a simple boolean
at index time: `"true"` → true, everything else → false. It does not perform
the `auto` scan at discovery time (comment in `resolveHumanInputField`: "the
daemon cannot perform dynamic auto-detection at discovery time"). For YAML
workflows, the `auto` scan is cheap — the file is already parsed — so the
registry could resolve `auto` immediately for YAML scopes. Alternatively, keep
the existing behavior (treat `auto` as false at index time) for consistency
across all scope types. Either approach is acceptable; the choice should be
consistent.

## Parsing Design

There are two components that currently parse YAML workflow files and manifest
files, and they explicitly reject each other's format:

- **`manifest.ParseManifestData`** returns `ErrNotManifest` when `states` is
  present.
- **`yamlscope.Parse`** only reads `states` and ignores other top-level keys.

The implementation needs a way to extract manifest metadata from a YAML file
that also contains `states`. Options include:

- A new function that extracts manifest fields from a YAML scope file
  (e.g., `manifest.ParseFromYamlScope` or `yamlscope.ExtractManifest`).
- Modifying the existing parsers to handle the combined case.
- Having the registry do its own lightweight YAML parsing to extract just `id`
  and the other manifest fields.

The key constraint is: `yamlscope.Parse` must not break — it is the parser used
at runtime by the orchestrator, executors, lint, and diagram. It should
continue to return a `YamlWorkflow` with only the state definitions. Manifest
fields are metadata consumed by the registry, not by the runtime.

## Validation

- `id` must be non-empty when present. A YAML file with `id: ""` alongside
  `states` should produce a validation error (not silently skip).
- `requires_human_input` must be one of `auto`, `true`, `false` when present.
- `id` values must be unique across all discovered workflows (directories, zips,
  and YAML files). If two workflows in the same registry have the same `id`,
  the registry should report an error or last-wins (match existing behavior for
  directory/zip conflicts).
- Manifest fields in a YAML workflow do not affect state validation. A YAML
  file with `id` but invalid states should fail state validation, not manifest
  validation.
- Unknown top-level keys (neither manifest fields nor `states`) should be
  ignored, not rejected. This preserves forward compatibility and allows
  authors to add comments or custom metadata.

## Interaction with Other Scope Types

### Zip archives

Zip archives already contain `workflow.yaml` **inside** the archive — the
manifest is part of the package. No changes needed.

### Directory workflows

Directory workflows have a separate `workflow.yaml` manifest file alongside
their state files. No changes needed.

### YAML workflow inside a directory that also has a `workflow.yaml` manifest

This is an edge case: a root directory contains both a standalone
`workflow.yaml` (manifest for the directory scope) and a `review.yaml` (YAML
scope workflow). The registry should discover both — the directory workflow via
`tryIndexDir` and the YAML workflow via the new YAML branch. They would have
different `id` values (from their respective manifest sources) and different
`ScopeDir` values (the directory vs. the YAML file path).

### YAML workflow named `workflow.yaml`

A YAML scope file named `workflow.yaml` (with both `states` and `id`) placed
directly in a root directory would be found by both the directory scanner (as a
potential manifest) and the new YAML scanner. The directory scanner would call
`ParseManifest`, which returns `ErrNotManifest` (because `states` is present),
so it would skip it. The YAML scanner would find it and index it. This is the
correct behavior — no conflict.

However, a YAML scope file named `workflow.yaml` placed inside a subdirectory
(alongside `.md` state files) is already a known edge case. The manifest parser
returns `ErrNotManifest` and the orchestrator falls back to frontmatter
scanning. This feature does not change that behavior — the YAML scanner only
looks at immediate children of root directories, not files inside
subdirectories.

## Documentation

`docs/yaml-workflows.md` should be updated to document the optional manifest
fields. A new section (e.g., "Manifest Metadata for Daemon Discovery") should
explain which fields are recognized, that `id` is required for discoverability,
and show an example of a self-describing YAML workflow.

`docs/daemon-server.md` should mention YAML workflows as a third discoverable
scope type alongside directories and zip archives.

The comparison table in `docs/yaml-workflows.md` should add a row for manifest
support:

| | Directory | ZIP | YAML |
|---|-----------|-----|------|
| **Manifest** | Separate `workflow.yaml` file | `workflow.yaml` inside archive | Embedded in same file |

## Example: Complete Self-Describing YAML Workflow

```yaml
id: vendor-approval
name: Vendor Approval
description: Evaluates a vendor proposal and routes through human approval
default_budget: 5.0
input_schema:
  vendor_name: string
  budget_limit: string
requires_human_input: auto

states:
  1_START:
    prompt: |
      Research the vendor "{{result}}" and prepare an analysis covering
      price, support SLA, and references.

      STOP after writing your analysis. Do not make a recommendation yet.
    allowed_transitions:
      - tag: await
        next: DECISION.md
        timeout: "48h"
        timeout_next: ESCALATE.md

  DECISION:
    prompt: |
      The reviewer's decision: {{result}}

      Execute the decision and produce a final report in vendor_report.md.
    allowed_transitions:
      - tag: result

  ESCALATE:
    prompt: |
      The vendor decision timed out. Send an escalation notice and close.
    allowed_transitions:
      - tag: result
```

This file is everything: workflow definition, metadata, and daemon
discoverability. It can be run directly (`raymond workflow.yaml`), discovered
by the daemon (`raymond serve --root .`), linted (`raymond lint workflow.yaml`),
and diagrammed (`raymond diagram workflow.yaml`).
