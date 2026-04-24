# Raymond

Raymond is a workflow orchestrator for AI coding agents like Claude Code. It
chains multiple prompts into multi-step workflows with intelligent context
management — controlling what the agent remembers, forgets, and passes between
steps.

Claude Code excels at executing toward a single goal, but isn't built to chain
tasks, run continuous loops, or manage context across phases. Raymond fills
that gap by treating workflows as state machines where each state is a markdown
prompt or shell script, and transitions are declared within the prompts
themselves.

## Key features

- **State machine workflows** — Define multi-step workflows as directories of
  markdown prompts and shell scripts that reference each other via transition
  tags
- **Context control** — Seven transition tags (`goto`, `reset`, `call`,
  `function`, `fork`, `await`, `result`) give precise control over what
  context carries forward, gets discarded, branches, pauses for input, or
  terminates the current state
- **Human-in-the-loop** — The `<await>` transition suspends an agent and
  presents a prompt to a human; the workflow resumes when input arrives
- **Shell script states** — Deterministic operations (polling, builds, data
  processing) run as shell scripts with zero token cost
- **Multi-agent** — Fork independent agents that run in parallel within the
  same workflow
- **Cross-workflow calls** — Invoke other workflows via `<call-workflow>`,
  `<function-workflow>`, `<fork-workflow>`, and `<reset-workflow>` with shared
  budget and state
- **Daemon mode** — `raymond serve` exposes workflows via HTTP API and MCP
  tools, with a web UI for monitoring runs and delivering human input
- **Skill packaging** — Bundle workflows as self-contained skills with a
  contract file (SKILL.md), entry point script, and manifest for daemon
  discovery
- **Cost budgets** — Set per-workflow spending limits that override transitions
  when exceeded
- **Crash recovery** — Workflow state is persisted to disk; crashed workflows
  can be resumed
- **Static analysis** — `raymond lint` validates workflows statically;
  `raymond diagram` generates Mermaid flowcharts; `raymond convert` turns a
  directory or zip workflow into the single-file YAML format

## Quick start

```bash
# Build (requires Go 1.21+)
go build -o raymond ./cmd/raymond
go build -o ray ./cmd/ray

# Or install to GOPATH/bin
go install ./cmd/raymond ./cmd/ray

# Run a workflow
raymond workflows/test_cases/CLASSIFY.md

# Run with options
raymond workflows/test_cases/START.md --budget 5.0 --model sonnet

# Resume a workflow
raymond --resume workflow_2026-01-15_14-30-22

# Run a workflow with human-in-the-loop support
raymond workflow/ --on-await=pause
# If it exits with code 2, deliver input and resume:
raymond --resume <run_id> --input "approved"

# Start the daemon (HTTP API + web UI)
raymond serve --root ./workflows

# Lint, diagram, and convert
raymond lint ./my-workflow
raymond diagram --html ./my-workflow
raymond convert ./my-workflow --output my-workflow.yaml

# Generate a config file
raymond --init-config

# Or generate one with permissive defaults (budget=1000,
# dangerously-skip-permissions=true) — use only in trusted/sandboxed envs
raymond --init-unsafe-defaults
```

## Example workflow

A simple two-step workflow that plans and then implements:

**workflows/example/PLAN.md:**
```markdown
---
allowed_transitions:
  - { tag: reset, target: IMPLEMENT.md }
---
Read the issue description in issue.md and create a plan in plan.md.

When the plan is ready, emit <reset>IMPLEMENT.md</reset>
```

**workflows/example/IMPLEMENT.md:**
```markdown
---
allowed_transitions:
  - { tag: goto, target: IMPLEMENT.md }
  - { tag: result }
---
Implement the feature according to plan.md. Run the tests.

If tests fail, fix and try again: <goto>IMPLEMENT.md</goto>
When everything passes: <result>Implementation complete</result>
```

```bash
raymond workflows/example/PLAN.md
```

The `<reset>` discards the planning context (it's captured in plan.md) and
starts implementation fresh. The `<goto>` loop preserves context across retries
so the agent can see its previous attempts.

## Documentation

| Document | Audience | Description |
|----------|----------|-------------|
| [Authoring Guide](docs/authoring-guide.md) | Workflow authors | How to write state files — the complete guide |
| [Skill Packaging](docs/skill-packaging.md) | Workflow authors | Bundle workflows as skills with SKILL.md, run.sh, and manifest |
| [Workflow Protocol](docs/workflow-protocol.md) | Reference | Authoritative protocol specification |
| [Daemon Server](docs/daemon-server.md) | Reference | `raymond serve` — HTTP API, MCP tools, and web UI |
| [Lint](docs/lint.md) | Reference | `raymond lint` — static analysis checks |
| [Diagram](docs/diagram.md) | Reference | `raymond diagram` — Mermaid flowchart generation |
| [Cross-Workflow Design](docs/cross-workflow-design.md) | Reference | Cross-workflow invocation tags |
| [YAML Workflows](docs/yaml-workflows.md) | Reference | Single-file YAML workflow format |
| [Reset Stack Retention](docs/reset-stack-retention.md) | Reference | How `<reset>` preserves the call/return stack |
| [Terminal Title Bar](docs/terminal-titlebar.md) | Reference | Terminal title updates during workflow execution |
| [Orchestration Design](docs/orchestration-design.md) | Raymond developers | Architecture and internal design |
| [Script States Design](docs/bash-states.md) | Raymond developers | Design rationale for shell script states |
| [Implementation Assumptions](docs/implementation-assumptions.md) | Raymond developers | Design decision log |
| [Configuration Design](docs/configuration-file-design.md) | Raymond developers | Configuration system design |
| [Code Structure](docs/code-structure.md) | Raymond developers | Project structure and development setup |
| [Sample Workflows](docs/sample-workflows.md) | Both | Test workflows and examples |

## Platform support

- **Production**: Linux (typically in a container for containment)
- **Development**: Windows and Linux supported; some examples use Linux commands

## License

MIT
