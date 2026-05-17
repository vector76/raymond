# Raymond

Raymond is a workflow orchestrator for AI coding agents. It chains multiple
prompts into multi-step workflows with intelligent context management —
controlling what the agent remembers, forgets, and passes between steps.
Workflows run against Claude Code by default, or against `pi` (the
multi-provider pi coding agent) when declared in the manifest.

Coding agents excel at executing toward a single goal, but aren't built to
chain tasks, run continuous loops, or manage context across phases. Raymond
fills that gap by treating workflows as state machines where each state is a
markdown prompt or shell script, and transitions are declared within the
prompts themselves.

## Key features

- **State machine workflows** — Define multi-step workflows as directories of
  markdown prompts and shell scripts that reference each other via transition
  tags
- **Context control** — Seven transition tags (`goto`, `reset`, `call`,
  `function`, `fork`, `ask`, `result`) give precise control over what
  context carries forward, gets discarded, branches, pauses for input, or
  terminates the current state
- **Human-in-the-loop** — The `<ask>` transition suspends an agent and
  presents a prompt to a human; the workflow resumes when input arrives
- **Shell script states** — Deterministic operations (polling, builds, data
  processing) run as shell scripts with zero token cost
- **Multi-agent** — Fork independent agents that run in parallel within the
  same workflow
- **Cross-workflow calls** — Invoke other workflows via `<call-workflow>`,
  `<function-workflow>`, `<fork-workflow>`, and `<reset-workflow>` with shared
  budget and state
- **Pluggable backend** — Workflows run against Claude Code by default; opt
  into the pi backend (multi-provider, supports Anthropic, OpenAI, Google,
  and more) by declaring `backend: pi` in the manifest
- **Daemon mode** — `ray serve` exposes workflows via HTTP API and MCP
  tools, with a web UI for monitoring runs and delivering human input
- **Skill packaging** — Bundle workflows as self-contained skills with a
  contract file (SKILL.md), entry point script, and manifest for daemon
  discovery
- **Cost budgets** — Set per-workflow spending limits that override transitions
  when exceeded
- **Crash recovery** — Workflow state is persisted to disk; crashed workflows
  can be resumed
- **Static analysis** — `ray lint` validates workflows statically;
  `ray diagram` generates Mermaid flowcharts; `ray convert` turns a
  directory or zip workflow into the single-file YAML format

## Quick start

```bash
# Build (requires Go 1.21+)
go build -o ray ./cmd/ray

# Or install to GOPATH/bin
go install ./cmd/ray

# Run a workflow
ray workflows/test_cases/CLASSIFY.md

# Run with options
ray workflows/test_cases/START.md --budget 5.0 --model sonnet

# Resume a workflow
ray --resume workflow_2026-01-15_14-30-22

# Run a workflow with human-in-the-loop support
ray workflow/ --on-ask=pause
# If it exits with code 2, deliver input and resume:
ray --resume <run_id> --input "approved"

# Start the daemon (HTTP API + web UI)
ray serve --root ./workflows

# Lint, diagram, and convert
ray lint ./my-workflow
ray diagram --html ./my-workflow
ray convert ./my-workflow --output my-workflow.yaml

# Generate a config file
ray --init-config
```

By default Raymond runs Claude with `--dangerously-skip-permissions` and an
unlimited budget. Both can be tightened either on the command line
(`--dangerously-skip-permissions=false`, `--budget=5`) or in
`.raymond/config.toml` (`dangerously_skip_permissions = false`,
`budget = 5`). Only run Raymond in environments you trust to skip
permission prompts.

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
ray workflows/example/PLAN.md
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
| [Daemon Server](docs/daemon-server.md) | Reference | `ray serve` — HTTP API, MCP tools, and web UI |
| [Lint](docs/lint.md) | Reference | `ray lint` — static analysis checks |
| [Diagram](docs/diagram.md) | Reference | `ray diagram` — Mermaid flowchart generation |
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
