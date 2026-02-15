# Raymond

Raymond is a workflow orchestrator for AI coding agents like Claude Code. It
chains multiple prompts into multi-step workflows with intelligent context
management — controlling what the agent remembers, forgets, and passes between
steps.

Claude Code excels at executing toward a single goal, but isn't built to chain
tasks, run continuous loops, or manage context across phases without human
interaction. Raymond fills that gap by treating workflows as state machines
where each state is a markdown prompt or shell script, and transitions are
declared within the prompts themselves.

## Key features

- **State machine workflows** — Define multi-step workflows as directories of
  markdown prompts and shell scripts that reference each other via transition
  tags
- **Context control** — Five invocation patterns (`goto`, `reset`, `call`,
  `function`, `fork`) give precise control over what context carries forward,
  gets discarded, or branches
- **Shell script states** — Deterministic operations (polling, builds, data
  processing) run as shell scripts with zero token cost
- **Multi-agent** — Fork independent agents that run in parallel within the
  same workflow
- **Cost budgets** — Set per-workflow spending limits that override transitions
  when exceeded
- **Crash recovery** — Workflow state is persisted to disk; crashed workflows
  can be resumed
- **Debug mode** — Full execution history preserved for analysis (enabled by
  default)

## Quick start

```bash
# Install (requires Python 3.11+)
pip install -e .
pip install -r requirements.txt

# Run a workflow
raymond workflows/test_cases/CLASSIFY.md

# Run with options
raymond workflows/test_cases/START.md --budget 5.0 --model sonnet

# Resume a workflow
raymond --resume workflow_2026-01-15_14-30-22

# Generate a config file
raymond --init-config
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
| [Workflow Protocol](docs/workflow-protocol.md) | Reference | Authoritative protocol specification |
| [Orchestration Design](docs/orchestration-design.md) | Raymond developers | Architecture and internal design |
| [Script States Design](docs/bash-states.md) | Raymond developers | Design rationale for shell script states |
| [Implementation Assumptions](docs/implementation-assumptions.md) | Raymond developers | Design decision log |
| [Configuration Design](docs/configuration-file-design.md) | Raymond developers | Configuration system design |
| [Console Output Design](docs/console-output.md) | Raymond developers | Console output format design |
| [Debug Mode](docs/debug-mode.md) | Raymond developers | Debug mode feature design |
| [Code Structure](docs/code-structure.md) | Raymond developers | Project structure and development setup |
| [Sample Workflows](docs/sample-workflows.md) | Both | Test workflows and examples |

## Platform support

- **Production**: Linux (typically in a container for containment)
- **Development**: Windows and Linux supported; some examples use Linux commands

## License

MIT
