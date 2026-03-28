# Code Organization Structure

## Overview

This document defines the code organization for the raymond project, written in Go.

## Project Structure

```
raymond/
├── README.md                    # Project overview and quick start
├── AGENTS.md                    # Agent instructions (authoritative)
├── CLAUDE.md                    # Copy of AGENTS.md for tooling compatibility
├── go.mod                       # Go module definition (github.com/vector76/raymond)
├── go.sum                       # Dependency checksums
│
├── cmd/                         # Binary entry points
│   ├── raymond/
│   │   └── main.go              # Main raymond binary (full workflow runner)
│   └── ray/
│       └── main.go              # ray alias (thin wrapper around raymond)
│
├── internal/                    # Private packages (not importable externally)
│   ├── bus/                     # Event bus (publish/subscribe)
│   ├── ccwrap/                  # Claude Code CLI wrapper (streaming JSON)
│   ├── cli/                     # Command-line interface (cobra)
│   ├── config/                  # .raymond/config.toml configuration file handling
│   ├── diagram/                 # Mermaid flowchart and HTML generation
│   ├── events/                  # Event type definitions (StateStarted, etc.)
│   ├── executors/               # State executors (markdown and script)
│   ├── lint/                    # Workflow static analysis (lint checks)
│   ├── observers/               # Event subscribers that produce output
│   │   ├── console/             # Human-readable terminal output
│   │   ├── debug/               # Debug JSONL files per step
│   │   └── titlebar/            # Terminal title bar updates
│   ├── orchestrator/            # Workflow orchestration loop
│   ├── parsing/                 # Transition tag parsing (<goto>, <result>, etc.)
│   ├── platform/                # Cross-platform script execution (.sh / .bat)
│   ├── policy/                  # Model and effort policy resolution
│   ├── prompts/                 # Prompt file loading and state name resolution
│   ├── registry/                # Remote workflow URL resolution and local cache
│   ├── specifier/               # Workflow path/URL specifier parsing and entry-point resolution
│   ├── state/                   # Workflow state persistence (JSON files)
│   ├── transitions/             # Transition application logic (all 6 types)
│   ├── version/                 # Build version string
│   ├── workflow/                # State file enumeration, data extraction, and graph traversal
│   ├── yamlscope/               # Single-file YAML workflow scope support
│   └── zipscope/                # ZIP archive workflow scope support
│
├── tests/
│   └── integration/             # End-to-end integration tests (build tag: integration)
│
├── docs/                        # Architecture and design documentation (flat structure)
│
└── workflows/
    └── test_cases/              # Workflow fixture files used by integration tests
```

## Key Principles

### Separation of Concerns

- **`cmd/`**: Thin entry points only — delegate to `internal/cli` immediately
- **`internal/`**: All production logic; packages are independently testable
- **`tests/integration/`**: End-to-end tests gated behind the `integration` build tag
- **`docs/`**: Architecture and design documentation (flat structure, no subfolders)
- **Root**: Module definition, project-level docs, and agent instructions

### Internal Package Architecture

The internal packages form a layered dependency graph:

```
cmd/raymond  cmd/ray
     └──────────┬──────────┘
                │
          internal/cli
        / /   /   \   \   \
       / /    |    \   \   \
  lint diagram  |  workflow specifier registry
                |
        internal/orchestrator
             /           \
      internal/executors  internal/transitions
        /       \                   |
    ccwrap    platform         internal/state
        \              \            |
         \         internal/parsing |
          \                         |
           ──── internal/events ────
                      │
                internal/bus
                      │
               internal/observers
                (console/debug/titlebar)
```

Lower-level packages (`events`, `bus`, `parsing`, `state`) have no dependencies
on higher-level orchestration code.

### Package Responsibilities

| Package | Responsibility |
|---------|---------------|
| `bus` | Typed publish/subscribe event bus with panic recovery |
| `ccwrap` | Spawn and stream `claude` CLI output; parse JSONL; handle timeouts |
| `cli` | Parse CLI flags, load config, wire observers, call orchestrator |
| `config` | Load and merge `.raymond/config.toml` configuration files |
| `diagram` | Generate Mermaid flowchart text and interactive HTML from a workflow scope |
| `events` | All event struct definitions shared across the codebase |
| `executors` | `MarkdownExecutor` (LLM) and `ScriptExecutor` (.sh/.bat); `ExecutionContext` |
| `lint` | Static analysis of workflow definitions; produces typed diagnostics |
| `observers/console` | Print workflow progress to the terminal |
| `observers/debug` | Write per-step JSONL debug files to a debug directory |
| `observers/titlebar` | Update the terminal title bar with workflow status |
| `orchestrator` | Main loop: load state, pick next agent, execute, apply transition, persist |
| `parsing` | Parse `<goto>`, `<reset>`, `<call>`, `<fork>`, `<function>`, `<result>` tags |
| `platform` | Build and run `exec.Cmd` for .sh (bash) or .bat (cmd.exe); merge env |
| `policy` | Resolve effective model and effort level from config + defaults |
| `prompts` | Load prompt files from directory, ZIP, or YAML scope; resolve state names |
| `registry` | Local cache for workflow zip files downloaded from URLs; validates SHA256 hashes |
| `specifier` | Resolve raw workflow specifier strings to absolute scope directories and entry points |
| `state` | Read/write `WorkflowState` JSON; atomic writes via temp+rename |
| `transitions` | Apply each of the 6 transition types to `WorkflowState` |
| `version` | Build version string injected at link time |
| `workflow` | List state files, read content from directory/ZIP/YAML scopes, extract transitions and policy data, and BFS graph helpers |
| `yamlscope` | Treat single-file YAML workflows as virtual workflow scope directories |
| `zipscope` | Treat ZIP archives as read-only workflow scope directories |

## Testing

### Unit Tests

Each `internal/<package>/` directory contains a `<package>_test.go` file (and
sometimes an `export_test.go` to expose internal symbols for testing).

```bash
go test ./...                    # Run all unit tests
go test ./internal/orchestrator/ # Run tests for a single package
go test -v ./internal/ccwrap/    # Verbose output
go test -run TestFoo ./...       # Run tests matching a name pattern
```

### Integration Tests

Integration tests live in `tests/integration/` and are gated behind the
`integration` build tag so they are excluded from the normal `go test ./...`
run.

```bash
# Script-only tests (no claude CLI needed)
go test -tags integration ./tests/integration/ -run TestScript

# All integration tests (requires claude CLI in PATH)
go test -tags integration ./tests/integration/
```

Tests that require the Claude CLI call `skipIfNoClaude(t)` to skip gracefully
when `claude` is not installed.

## Building

```bash
# Build both binaries
go build ./cmd/raymond
go build ./cmd/ray

# Build to a specific output path
go build -o /usr/local/bin/raymond ./cmd/raymond

# Verify everything compiles (no output produced)
go build ./...
```

## Running the Application

```bash
# Run a workflow (directory, YAML, or ZIP scope)
raymond path/to/workflow/START.md
raymond workflow.yaml
raymond workflow.yaml/REVIEW

# Resume a paused workflow
raymond --resume workflow-id

# Run without debug logging
raymond --no-debug path/to/workflow/START.md

# Run quietly (suppress progress output)
raymond --quiet path/to/workflow/START.md

# Lint a workflow for static analysis issues
raymond lint path/to/workflow/
raymond lint workflow.yaml
raymond lint --json --level error path/to/workflow/

# Generate a workflow diagram
raymond diagram path/to/workflow/
raymond diagram workflow.yaml
raymond diagram --html --output my-diagram.html path/to/workflow/
```

## Platform Support

- **Primary platform**: Linux (recommended for production use)
- **Development**: Windows is supported; `.bat` scripts run via `cmd.exe /c`,
  `.sh` scripts run via `bash`
- **macOS**: Supported for development

### Cross-Platform Script Execution

The `platform` package selects the shell based on file extension and OS:

| Extension | Unix | Windows |
|-----------|------|---------|
| `.sh` | `bash <script>` | error |
| `.bat` | error | `cmd.exe /c <script>` |

### Windows WSL Integration

When developing on Windows, WSL can run `.sh` scripts and integration tests:

```bash
# Run integration tests via WSL
wsl -- bash -l -c "cd /mnt/c/path/to/raymond && go test -tags integration ./tests/integration/"
```

**CRLF line endings**: Shell scripts committed on Windows may have CRLF endings.
The `.gitattributes` file enforces `*.sh text eol=lf` to prevent this.

## Dependencies

Dependencies are declared in `go.mod` and pinned in `go.sum`:

| Dependency | Purpose |
|-----------|---------|
| `github.com/spf13/cobra` | CLI flag parsing and subcommand structure |
| `github.com/BurntSushi/toml` | `.raymond.toml` configuration file parsing |
| `gopkg.in/yaml.v3` | YAML parsing for workflow scopes and frontmatter |
| `github.com/stretchr/testify` | Test assertions (`assert`, `require`) |

No runtime dependencies beyond the Go standard library and the above four
packages.
