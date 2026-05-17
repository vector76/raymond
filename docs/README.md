# Raymond Documentation

This directory holds the long-form documentation for Raymond. The top-level
[../README.md](../README.md) is the project overview and quick start; the
documents here go deeper. Pick the path that matches what you're trying to do.

## I want to write a workflow

Start here:

1. [authoring-guide.md](authoring-guide.md) — the complete guide to writing
   states, transitions, and scoping. Read this first.
2. [sample-workflows.md](sample-workflows.md) — worked examples covering every
   orchestration pattern.
3. [yaml-workflows.md](yaml-workflows.md) — single-file YAML workflow format.
4. [skill-packaging.md](skill-packaging.md) — bundle a workflow as a
   distributable skill (SKILL.md + run.sh + manifest).

When you need to look something up:

- [workflow-protocol.md](workflow-protocol.md) — authoritative reference for
  every transition tag, the return stack, and `allowed_transitions`.
- [lint.md](lint.md) — `ray lint` static checks and how to interpret them.
- [diagram.md](diagram.md) — `ray diagram` flowchart generation.
- [cross-workflow-design.md](cross-workflow-design.md) — `<call-workflow>`,
  `<function-workflow>`, `<fork-workflow>`, `<reset-workflow>`.
- [pi-backend.md](pi-backend.md) — running workflows against pi instead of
  Claude Code (including rationale and behavioral differences).
- [configuration-file-design.md](configuration-file-design.md) — `.raymond/config.toml`
  discovery and precedence.

## I'm operating `ray serve`

- [daemon-server.md](daemon-server.md) — HTTP API, workflow registry,
  run lifecycle.
- [serve-run-pool.md](serve-run-pool.md) — why CLI runs and daemon runs use
  separate state pools.
- [graceful-shutdown.md](graceful-shutdown.md) — signal handling and the
  two-phase shutdown protocol.
- [terminal-titlebar.md](terminal-titlebar.md) — OSC 2 title updates during
  execution.

## I'm contributing to Raymond

- [code-structure.md](code-structure.md) — project layout and internal package
  boundaries. Start here.
- [orchestration-design.md](orchestration-design.md) — the state-machine model,
  context discipline, and how Raymond compares to a Ralph loop.
- [workflow-protocol.md](workflow-protocol.md) — the protocol every backend and
  executor must honor.
- [design-decisions.md](design-decisions.md) — grab-bag of implementation
  decisions (state file location, ID format, atomicity, etc.) with rationale
  and alternatives considered.
- [bash-states.md](bash-states.md) — design rationale for shell-script states.
- [pi-backend.md](pi-backend.md) — pi backend specification and integration
  points.
- [ask-file-attachments-design.md](ask-file-attachments-design.md) — file
  upload/download design for `<ask>` steps.
- [reset-stack-retention.md](reset-stack-retention.md) — why `<reset>` now
  preserves the return stack.
- [agent-framework-comparison.md](agent-framework-comparison.md) — how Raymond
  positions against Hermes, Manus, CrewAI, and Sandcastle.

## Status conventions

Documents whose title says "Design" or "Design Rationale" generally describe a
shipped feature — they record *why* the feature was built the way it was, not
future work. Where it isn't obvious from context, a document declares its
status near the top. Forward-looking proposals, when they exist, are marked
explicitly.
