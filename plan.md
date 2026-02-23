# Go Port Plan: raymond

## Goal

Feature-identical port of the Python raymond workflow orchestrator to Go. Motivation: single-binary distribution on Linux, macOS, and Windows without requiring a Python runtime.

The Python implementation at `../raymond` is the authoritative reference throughout development. This plan assumes the reader is familiar with the architecture documented in `docs/`.

---

## Fixed Decisions

| Topic | Decision |
|---|---|
| Go module | `github.com/vector76/raymond` |
| CLI framework | cobra |
| Binary names | `raymond` and `ray` (both installed) |
| Template substitution | Manual string replace — `{{result}}` and fork attributes only; no template library |
| State file format | Independent from Python — no migration needed, capabilities must match |
| Platform support | Linux, macOS, Windows from day one |
| Script types | `.sh` → bash (Unix), `.bat` → cmd.exe (Windows); `.ps1` → PowerShell designed in, implementation deferred |
| Python source | Deleted at branch creation; `../raymond` is the reference |
| Python tests | Deleted with Python source; Go tests written fresh |
| Test fixtures | `workflows/test_cases/` retained — language-agnostic |
| Docs | `docs/` retained as-is |

---

## Branch Setup Steps

These steps are performed once to create the starting point:

1. Create branch `go-port` from `main`
2. Delete all Python source and tooling:
   - `src/`
   - `tests/`
   - `pyproject.toml`
   - `requirements.txt`
   - `main.py`
   - `bead_list.md`
   - `plan.md` (this file moves to `go-port-plan.md` before deletion, or is replaced in-place)
3. Update `.gitignore` — remove Python entries, add Go entries (`/raymond`, `/ray`, `*.exe`, `vendor/`)
4. Initialize Go module: `go mod init github.com/vector76/raymond`
5. Create directory skeleton (see Repository Structure below)
6. Add initial `go.sum` by running `go mod tidy`
7. Commit the clean starting state

---

## Repository Structure (target state)

```
raymond/
├── cmd/
│   ├── raymond/
│   │   └── main.go              # Entry point; delegates to internal/cli
│   └── ray/
│       └── main.go              # One-liner: calls raymond's Run()
│
├── internal/
│   ├── parsing/                 # Transition tag parsing
│   ├── policy/                  # YAML frontmatter + policy validation
│   ├── events/                  # Event type definitions
│   ├── bus/                     # Event pub/sub (observer registration + emit)
│   ├── config/                  # Config file loading (.raymond/config.*)
│   ├── state/                   # Workflow state persistence (JSON)
│   ├── platform/                # OS-specific subprocess setup (build-tag split)
│   │   ├── platform_unix.go     # (linux, darwin): Setsid
│   │   └── platform_windows.go  # CREATE_NEW_PROCESS_GROUP
│   ├── ccwrap/                  # Claude Code subprocess wrapper + streaming
│   ├── prompts/                 # Prompt file loading + template substitution
│   ├── executors/
│   │   ├── script/              # Shell/batch script executor
│   │   └── markdown/            # Claude Code markdown executor
│   ├── transitions/             # goto, reset, call, function, fork, result handlers
│   ├── orchestrator/            # Main workflow loop; goroutine-per-agent
│   ├── observers/
│   │   ├── console/             # Console output observer
│   │   ├── debug/               # Debug file logging observer
│   │   └── titlebar/            # Terminal titlebar observer
│   └── zipscope/                # ZIP archive workflow support
│
├── docs/                        # Architecture documentation (unchanged)
├── workflows/                   # Example and test-case workflows (unchanged)
├── AGENTS.md
├── CLAUDE.md
├── README.md
├── go.mod
└── go.sum
```

---

## Go Dependencies

| Package | Purpose |
|---|---|
| `github.com/spf13/cobra` | CLI framework |
| `gopkg.in/yaml.v3` | YAML frontmatter parsing |
| TOML library (verify) | Config file — verify format against Python reference; likely `github.com/BurntSushi/toml` if TOML, or reuse yaml.v3 if YAML |
| `github.com/stretchr/testify` | Test assertions (assert, require) |
| `archive/zip` (stdlib) | ZIP scope support |
| `encoding/json` (stdlib) | State persistence, streaming JSON decode |
| `os/exec`, `context`, `sync` (stdlib) | Subprocess management, cancellation, coordination |

---

## Implementation Phases

Each phase follows TDD: tests are written alongside or before implementation. Each package's tests live in `*_test.go` files adjacent to the source. The full suite runs with `go test ./...`.

---

### Phase 0: Branch & Scaffold

**Deliverables:**
- Clean branch with Python removed, Go skeleton in place
- `go.mod` with all dependencies declared
- Empty package stubs so `go build ./...` succeeds
- CI-ready: `go test ./...` runs (zero tests, but compiles cleanly)

---

### Phase 1: Parsing (`internal/parsing/`)

Transition tag parser — pure functions, no dependencies, highest value for TDD start.

**What it does:**
- Accepts a string (agent output) and returns the parsed transition(s) found
- Extracts: tag type (`goto`, `reset`, `call`, `function`, `fork`, `result`), target filename, attributes (key/value pairs)
- Detects zero tags (error) and multiple tags (error) — caller decides how to respond
- Validates that targets contain no `/` or `\` (path traversal prevention)

**Key types:**
```go
type TagType string  // "goto", "reset", "call", "function", "fork", "result"

type ParsedTransition struct {
    Tag        TagType
    Target     string            // filename (empty for result)
    Attrs      map[string]string // e.g. return="X.md", cd="/path", item="x"
    Payload    string            // content between tags (result only)
}

type ParseResult struct {
    Transitions []ParsedTransition
    Err         error  // ErrNoTransition, ErrMultipleTransitions
}
```

**Tests:** Mirror the behavior covered by `tests/test_parsing.py` in the Python reference. Cover all tag types, attribute parsing, multi-tag error, no-tag error, tags embedded in surrounding text.

---

### Phase 2: Policy (`internal/policy/`)

YAML frontmatter parsing and transition validation.

**What it does:**
- Splits a markdown file into: YAML frontmatter block + body text
- Parses frontmatter into a policy struct (model override, allowed_transitions list)
- Validates a proposed transition against the policy
- Returns the body text for use as the prompt

**Key types:**
```go
type StateFrontmatter struct {
    Model               string
    AllowedTransitions  []TransitionRule
}

type TransitionRule struct {
    Tag     TagType
    Target  string  // empty means any
    Return  string  // for call/function
}
```

**Tests:** Mirror `tests/test_policy.py`. Cover: no frontmatter, empty allowed_transitions (all transitions allowed), specific allow lists, violations, call/function return target matching.

---

### Phase 3: Events & Bus (`internal/events/`, `internal/bus/`)

Event system that decouples orchestrator from observers.

**Events (`internal/events/`):**

Define all event types as Go structs implementing a common interface. Events mirror the Python `events.py` definitions exactly:
- `StateStarted`, `StateCompleted`
- `ClaudeStreamOutput`
- `ToolInvocation`
- `ScriptOutput`
- `ProgressMessage`
- `TransitionOccurred` (includes tag type, source, target, metadata including result payload for return transitions)
- `AgentSpawned`, `AgentTerminated`, `AgentPaused`
- `ErrorOccurred`
- `WorkflowStarted`, `WorkflowCompleted`

**Bus (`internal/bus/`):**

```go
type Observer interface {
    OnEvent(event events.Event)
}

type Bus struct { ... }
func (b *Bus) Register(o Observer)
func (b *Bus) Emit(e events.Event)
```

Synchronous delivery (same as Python). Thread-safety: Emit may be called from multiple goroutines; bus serializes delivery.

**Tests:** Basic registration and delivery. Order preservation. Multiple observers. Safe concurrent emit.

---

### Phase 4: Config (`internal/config/`)

Config file loading from `.raymond/` directory.

**What it does:**
- Locates config file (check Python reference for exact filename and format)
- Loads model defaults, timeout defaults, budget defaults, any other settings
- Merges with CLI flag overrides (config provides defaults; flags win)
- Returns a flat Config struct used throughout

**Note:** Verify config file format (TOML vs YAML) by reading `../raymond/src/config.py` before implementing. Choose appropriate Go library accordingly.

**Tests:** Mirror `tests/test_config.py`. Cover: missing config (defaults), partial config, full config, format errors.

---

### Phase 5: State Persistence (`internal/state/`)

Workflow state saved to disk for crash recovery.

**What it does:**
- Defines `WorkflowState` and `AgentState` structs (format independent from Python)
- Writes state atomically (write temp file, rename) after each transition
- Loads state by workflow ID for resume
- Lists all state files (for `--list` and `--recover`)
- Deletes state on successful completion

**Key types:**
```go
type AgentState struct {
    ID              string
    CurrentState    string
    SessionID       string
    Stack           []StackFrame
    CWD             string
    PendingResult   string
    ForkSessionID   string
    ForkAttributes  map[string]string
}

type WorkflowState struct {
    WorkflowID    string
    ScopeDir      string
    Agents        []AgentState
    TotalCostUSD  float64
    BudgetUSD     float64
    StartedAt     time.Time
    ForkCounters  map[string]int
}
```

**Tests:** Mirror `tests/test_state.py`. Cover: save/load round-trip, atomic write (simulate crash mid-write), list, delete, resume ID resolution.

---

### Phase 6: Platform Abstraction (`internal/platform/`)

OS-specific behavior isolated behind a stable API using Go build tags.

**Files:**
- `platform_unix.go` (build constraint: `//go:build !windows`)
- `platform_windows.go` (build constraint: `//go:build windows`)

**Functions exposed:**

```go
// Apply OS-specific subprocess attributes to prevent Claude TUI from
// controlling the terminal (Setsid on Unix, new process group on Windows).
func SetSubprocessAttrs(cmd *exec.Cmd)

// Route a script file to its interpreter based on extension and OS.
// Returns the command and arguments to execute.
// .sh  → ["bash", scriptPath]          (Unix)
// .bat → ["cmd.exe", "/C", scriptPath] (Windows)
// .ps1 → ["powershell", "-File", scriptPath] (both, future)
// Returns error if extension is unrecognized for current OS.
func ScriptCommand(scriptPath string) ([]string, error)
```

**Design note for PowerShell:** `ScriptCommand` uses a dispatch table (map of extension to builder function), not a switch statement. Adding `.ps1` support requires only adding an entry to the table in each platform file. No other code changes needed.

**Tests:** Platform-specific tests using build tags. Verify correct command construction per OS/extension combination. The Setsid test is Linux/macOS only.

---

### Phase 7: Claude Code Wrapper (`internal/ccwrap/`)

The most critical package. Manages the `claude` subprocess, streams JSON output, handles timeouts and session IDs.

**Interface (for testability):**

```go
type StreamEvent struct {
    Raw     []byte          // original JSON line
    Parsed  map[string]any  // decoded JSON
}

type InvokeOptions struct {
    Prompt                    string
    Model                     string
    SessionID                 string
    ForkSession               bool
    DangerouslySkipPermissions bool
    DisallowedTools           []string
    CWD                       string
    IdleTimeout               time.Duration // 0 = no timeout
}

type ClaudeInvoker interface {
    // Stream invokes claude and returns a channel of events.
    // The channel is closed when the process exits.
    // Returns the new session ID alongside the channel.
    // ctx cancellation terminates the subprocess.
    Stream(ctx context.Context, opts InvokeOptions) (<-chan StreamEvent, <-chan string, error)
}
```

**Real implementation (`SubprocessInvoker`):**
- Builds `claude` command: `-p`, `--output-format stream-json`, `--verbose`, `--model`, `--resume` (if session ID provided), `--fork-session` (if ForkSession), `--permission-mode acceptEdits` or `--dangerously-skip-permissions`, `--disallowed-tools`
- Strips `CLAUDECODE` from environment
- Calls `platform.SetSubprocessAttrs(cmd)` for terminal isolation
- Sets `stdin = os.DevNull`
- Reads stdout line by line; each line decoded as JSON and sent on channel
- Idle timeout: a timer resets on each received line; fires if no data for `IdleTimeout` duration
- On timeout: terminate subprocess, close channel with error
- Session ID: scanned from each JSON event (`session_id` field or `metadata.session_id`); last found value sent on session channel when process exits

**Mock implementation (`MockInvoker`):**
- Accepts a sequence of `[]StreamEvent` to emit
- Used in all executor and orchestrator unit tests
- Controllable: can simulate timeouts, non-zero exits, specific JSON sequences

**Tests:** Test with mock subprocess (shell script that emits fake JSON lines). Cover: streaming delivery order, idle timeout fires correctly, idle timeout resets on data, session ID extraction, non-zero exit code, ctx cancellation terminates process cleanly.

---

### Phase 8: Prompts (`internal/prompts/`)

Prompt file loading and template variable substitution.

**What it does:**
- Loads a state file from the scope (directory or zip) by filename
- Splits frontmatter from body (delegates to `internal/policy`)
- Applies substitutions to body text:
  - `{{result}}` → replaced with the provided result payload string
  - `{{KEY}}` → replaced with fork attribute values (e.g. `item`, `cd`, custom attrs)
  - Unrecognized `{{...}}` patterns left as-is (or error — verify Python behavior)
- Returns: body text after substitution, frontmatter policy

**Implementation note:** Use simple `strings.ReplaceAll` — no template library. The substitution set is closed and defined by the protocol.

**Tests:** Mirror `tests/test_prompts.py`. Cover: no substitution needed, result substitution, fork attribute substitution, missing attribute behavior, frontmatter separation.

---

### Phase 9: Executors (`internal/executors/`)

#### Script Executor (`internal/executors/script/`)

Executes shell/batch scripts deterministically. No LLM, no retry.

**What it does:**
- Calls `platform.ScriptCommand(path)` to get the interpreter command
- Sets `RAYMOND_*` environment variables (verify full list from `../raymond/src/orchestrator/executors/script.py`)
- Runs subprocess, captures stdout and stderr
- Parses transition from stdout using `internal/parsing`
- Emits `ScriptOutput` event for each line
- Returns `ExecutionResult` — zero cost, session ID unchanged
- Script errors are fatal (no reminder prompt, no retry)

#### Markdown Executor (`internal/executors/markdown/`)

Executes Claude Code states via `ccwrap.ClaudeInvoker`.

**What it does:**
- Loads and substitutes prompt (via `internal/prompts`)
- Invokes Claude via injected `ClaudeInvoker`
- Streams events, emitting `ClaudeStreamOutput` and `ToolInvocation` events to bus
- After stream ends: parses transition from final assistant message text
- If no transition or multiple transitions: emits reminder prompt, retries (max 3 attempts)
- If policy violation: emits policy reminder, retries
- Extracts cost from JSON response; adds to running total
- Enforces budget: if total exceeds budget, returns budget-exceeded error
- Returns `ExecutionResult` with new session ID and parsed transition

**Key type:**
```go
type ExecutionResult struct {
    Transition  parsing.ParsedTransition
    SessionID   string
    CostUSD     float64
    Err         error
}
```

**Tests:** Use `MockInvoker`. Cover: clean transition, no-transition → reminder → success, multiple transitions → reminder → success, policy violation retry, max retries exceeded, budget exceeded, cost accumulation, session ID threading.

---

### Phase 10: Transitions (`internal/transitions/`)

Applies a parsed transition to an agent's state, producing the next state.

**What it does** — one handler per tag type:

| Tag | Handler behavior |
|---|---|
| `goto` | Update `CurrentState`; keep `SessionID` and stack |
| `reset` | Update `CurrentState`; clear `SessionID`; clear stack; optionally update `CWD` from `cd` attr |
| `call` | Push frame (current session + return state) onto stack; update `CurrentState`; fork session for new branch |
| `function` | Push frame (nil session + return state) onto stack; update `CurrentState`; clear `SessionID` (fresh context) |
| `fork` | Create new `AgentState`; new agent gets target state and optional `cd`/attrs; caller continues to `next` attr |
| `result` (empty stack) | Mark agent terminated; emit `AgentTerminated` |
| `result` (non-empty stack) | Pop frame; restore session from frame; set `CurrentState` to frame's return state; store payload as `PendingResult` |

**Tests:** Mirror `tests/test_transitions.py`. Cover each transition type, call/result round-trip, function/result round-trip, fork spawns correct child state, result with empty stack terminates, stack depth > 1.

---

### Phase 11: Orchestrator (`internal/orchestrator/`)

Main workflow loop. Coordinates all agents.

**Concurrency model (as implemented):**
- Single-threaded round-robin loop: each iteration picks the next active agent and runs its current state to completion before moving to the next agent
- `context.Context` cancellation propagates to the running executor and its subprocesses
- State is persisted to disk after every step

**What it does:**
- Accepts initial workflow state
- Loops over active agents in round-robin order; executes each state and applies the resulting transition
- As `fork` transitions produce new agents, they join the rotation for subsequent rounds
- After each transition, persists state to disk (via `internal/state`)
- Enforces global budget across all agents
- Handles `AgentPaused` (rate limit): waits for reset time, resumes
- On error: emits `ErrorOccurred`; orchestrator decides fatal vs. recoverable
- On all agents terminated: emits `WorkflowCompleted`, deletes state file

**Tests:** Use `mockExec`. Cover: single linear workflow, call/return, fork and join (fork terminates independently), budget enforcement stops all agents, crash recovery resumes from state file, context cancellation.

---

### Phase 12: Observers (`internal/observers/`)

Observers implement `bus.Observer` and receive events from the bus.

#### Console Observer (`internal/observers/console/`)

Real-time workflow output to stdout. Mirrors the Python `ConsoleObserver` + `ConsoleReporter` design.

Output format is specified in `docs/console-output.md`. Key behaviors:
- State entry banner
- Streaming Claude output (pass-through)
- Transition arrows labeled with type: `  goto → NEXT.md`, `  return (snippet) → CALLER.md`
- Fork announcements
- Agent termination with result payload (whitespace-trimmed)
- Cost/budget display on completion
- Quiet mode suppresses progress messages

**Tests:** Mirror `tests/test_console.py` and `tests/test_observers.py`. Cover all transition types, return snippet truncation rules (trim → first line → 20-char limit), quiet mode, termination display.

#### Debug Observer (`internal/observers/debug/`)

Writes full execution history to `debug/` directory. Format: one JSON file per workflow, one JSONL entry per event.

**Tests:** Mirror `tests/test_debug_mode.py`. Cover file creation, event serialization, directory naming.

#### Titlebar Observer (`internal/observers/titlebar/`)

Updates terminal titlebar via ANSI escape codes with current workflow state.

**Tests:** Mirror `tests/test_observers.py` titlebar tests. Cover escape sequence format, update on state change, clear on completion.

---

### Phase 13: ZIP Scope (`internal/zipscope/`)

Allows workflows to be distributed as a single ZIP file.

**What it does:**
- Opens a ZIP archive and presents the same interface as a directory scope
- Resolves state filenames within the archive
- Extracts scripts to a temp directory before execution (scripts cannot run from inside a ZIP)
- Hash verification of archive integrity (verify whether Python version does this — check `../raymond/src/zip_scope.py`)
- Cleanup of temp extractions on exit

**Tests:** Mirror `tests/test_cli_zip.py` and related. Cover state resolution, script extraction, hash verification, cleanup.

---

### Phase 14: CLI (`cmd/raymond/`, `cmd/ray/`)

Cobra-based CLI exposing all commands and flags.

**Commands:**

| Command/Flag | Behavior |
|---|---|
| `raymond WORKFLOW.md` | Start new workflow from file or directory |
| `raymond --resume ID` | Resume workflow from state file |
| `raymond --list` | List all workflow state files |
| `raymond --status ID` | Show status of specific workflow |
| `raymond --recover` | List in-progress (non-completed) workflows |
| `raymond --init-config` | Write a template config file |

**Flags:**

| Flag | Default | Description |
|---|---|---|
| `--budget USD` | 10.00 | Cost limit |
| `--model` | config/sonnet | Claude model override |
| `--timeout SEC` | 600 | Idle timeout per invocation (0 = none) |
| `--dangerously-skip-permissions` | false | Skip Claude permission prompts |
| `--quiet` | false | Suppress progress messages |
| `--no-debug` | false | Disable debug observer |
| `--input TEXT` | "" | Initial `{{result}}` value |

**`cmd/ray/main.go`:** Imports the raymond CLI package and calls the same `Run()` entry point. Produces a separate binary with an identical feature set.

**Tests:** Mirror `tests/test_cli.py`. Cover argument parsing, command dispatch, flag defaults, error cases (file not found, invalid budget, etc.). CLI integration tests use `workflows/test_cases/`.

---

### Phase 15: End-to-End Integration Testing

Integration tests that run the full binary against real workflow fixtures.

**Test fixtures:** `workflows/test_cases/` — existing workflow files drive tests.

**Requirements:** `claude` CLI must be installed and authenticated. Tests that require `claude` are tagged and skipped if it's not available.

**What is tested:**
- Complete workflow execution from start to finish
- Crash recovery: kill process mid-workflow, resume, verify correct completion
- Budget enforcement: workflow stops at cost limit
- Multi-agent fork: parent and child both complete correctly
- All transition types exercised end-to-end
- ZIP scope: workflow packaged as ZIP runs correctly
- Both `raymond` and `ray` binaries produce identical results

**Platform matrix:** Tests should pass on Linux and macOS. Windows tests run where a CI environment is available; known platform-specific gaps are documented.

---

## Key Design Principles

### Context cancellation everywhere
Every goroutine accepts a `context.Context`. Cancellation propagates from the orchestrator through to subprocess termination. No goroutine leaks.

### Interface boundaries for testability
`ClaudeInvoker` is injected into executors and orchestrator — never constructed internally. This makes unit tests fast and deterministic. The same principle applies to any other external boundary (file I/O is wrapped where it aids testing).

### Platform from day one
`internal/platform/` is created in Phase 6 before any subprocess code is written. All subprocess construction goes through it. No OS-specific code outside this package.

### State is the source of truth
After every transition, state is persisted. The in-memory representation is always reconstructible from the state file. This makes crash recovery straightforward and makes the orchestrator stateless across restarts.

### No cross-package cycles
Dependency direction: `cmd` → `internal/orchestrator` → `internal/executors` → `internal/ccwrap`, `internal/platform`; all packages → `internal/events`, `internal/parsing`. The bus is passed by reference; observers register themselves; orchestrator emits without knowing which observers exist.
