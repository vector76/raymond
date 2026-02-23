# Code Review Findings

Generated 2026-02-23. All six review dimensions covered: doc/code alignment, intra-code consistency, test coverage, error handling, security/platform, architecture.

---

## CRITICAL

### C-01 · Cost Double-Accumulation Bug
**Source**: CODE-007
**Files**: `internal/executors/markdown.go:219-223`, `internal/orchestrator/orchestrator.go:261`
**Description**: `TotalCostUSD` is incremented twice per invocation. The markdown executor modifies `wfState.TotalCostUSD` directly (because `wfState` is a pointer to the same `WorkflowState`), then `orchestrator.go:261` adds `execResult.CostUSD` again. Every LLM invocation is counted twice.

```go
// markdown.go:219-223
invocationCost = ExtractCostFromResults(results)
if invocationCost > 0 {
    wfState.TotalCostUSD += invocationCost   // ← accumulates here…
}
// ExecutionResult.CostUSD also returns invocationCost

// orchestrator.go:261
ws.TotalCostUSD += execResult.CostUSD        // ← …and again here
```

**Fix**: Remove one of the two accumulations. Recommended: remove from executor, keep in orchestrator.

---

### C-02 · Idle-Timeout / Context-Timeout Race Condition
**Source**: ERR-001, ERR-004
**Files**: `internal/ccwrap/ccwrap.go:276-285`
**Description**: `select` on `ctx.Done()` and `idleExpired` are peers. When both fire simultaneously (e.g., total-timeout deadline fires while idle timer also fires), Go's runtime chooses non-deterministically. If `idleExpired` wins, the function returns `ClaudeCodeTimeoutError{Idle: true}` even though the context deadline was exceeded, causing incorrect error classification and retry behavior.

```go
select {
case <-ctx.Done():
    killAndDrain()
    return ctx.Err()
case <-idleExpired:             // can win the race
    killAndDrain()
    return &ClaudeCodeTimeoutError{Timeout: idleTimeout, Idle: true}
case msg, ok := <-lineCh:
```

**Fix**: Check `ctx.Err()` first inside the `idleExpired` case to prefer context cancellation.

---

### C-03 · Goroutine Leak in `InvokeStream`
**Source**: ERR-002
**Files**: `internal/ccwrap/ccwrap.go:172-184`
**Description**: The goroutine started in `InvokeStream` calls `runStream`, which loops emitting items to `ch`. If the caller stops reading (e.g., context cancelled), the goroutine blocks on `ch <-` indefinitely since the channel is unbuffered. The `select { case ch <- StreamItem{Err: err}: case <-ctx.Done(): }` only protects the final error send, not the stream loop itself.

**Fix**: Ensure the `runStream` inner loop also selects on `ctx.Done()` for every send to `ch`.

---

### C-04 · Agent Status Semantics Ambiguous
**Source**: CODE-001
**Files**: `internal/state/state.go:66`, `internal/orchestrator/orchestrator.go:358,402,415,424`
**Description**: `AgentState.Status` is documented as `"paused" | "failed" | ""` (empty = active). Agents created in `transitions.go:272-277` never explicitly set `Status`, relying on Go zero-value `""`. The code then checks `Status != "paused"` in `firstActiveIndex` and `allPaused` — this works by accident. A third state value (e.g., `"completed"`) would silently be treated as active.

**Fix**: Define string constants (`StatusActive`, `StatusPaused`, `StatusFailed`) and initialize `Status` explicitly on agent creation.

---

## HIGH

### H-01 · `--verbose` Flag Parsed but Never Used
**Source**: ARCH-016
**Files**: `internal/cli/cli.go:84,129`, `internal/config/config.go:370-373`, `internal/orchestrator/orchestrator.go:43-82`
**Description**: `--verbose` is declared, parsed into `CLIArgs.Verbose`, and merged through config, but `RunOptions` has no `Verbose` field. The flag is accepted silently and has zero effect.

**Fix**: Either add `Verbose bool` to `RunOptions` and wire it to the console observer (e.g., enable tool-call detail), or remove the flag entirely.

---

### H-02 · Debug Files World-Readable; May Contain Session IDs
**Source**: SEC-003, SEC-004, SEC-012
**Files**: `internal/observers/debug/debug.go:60,117`, `internal/executors/script.go:206`, `internal/orchestrator/orchestrator.go:525`
**Description**: Debug directories are created `0o755` and files `0o644` — readable by any user on the system. These files contain complete Claude JSON responses (including `session_id`), environment variables passed to scripts (including `RAYMOND_RESULT`), and workflow state. On a shared machine this exposes sensitive data.

**Fix**: Create debug directory as `0o700` and files as `0o600`.

---

### H-03 · `CLAUDECODE` Not Stripped from Script Subprocess Environment
**Source**: SEC-005
**Files**: `internal/ccwrap/ccwrap.go:68-79`, `internal/platform/platform.go:151-166`
**Description**: `ccwrap.BuildClaudeEnv` strips `CLAUDECODE` before invoking Claude subprocesses. `platform.mergeEnv` (used by script executor) does NOT strip it. Scripts therefore inherit `CLAUDECODE` from the parent environment, which can cause unexpected behavior if scripts invoke `claude` themselves.

**Fix**: Strip `CLAUDECODE` in `platform.mergeEnv` (mirror the logic in `BuildClaudeEnv`).

---

### H-04 · Concurrent Map Access in `ExecutionContext.GetNextStepNumber`
**Source**: ERR-009
**Files**: `internal/executors/executors.go:40-48`
**Description**: `StepCounters` is a `map[string]int` accessed without any synchronization. While execution is currently sequential, nothing prevents future parallel execution or concurrent test usage from triggering a race.

```go
func (c *ExecutionContext) GetNextStepNumber(agentID string) int {
    if c.StepCounters == nil {
        c.StepCounters = make(map[string]int)
    }
    c.StepCounters[agentID]++   // no mutex
    return c.StepCounters[agentID]
}
```

**Fix**: Add a `sync.Mutex` to `ExecutionContext` and lock around map access.

---

### H-05 · Debug Directory Creation Failure Silently Swallowed
**Source**: ERR-005
**Files**: `internal/orchestrator/orchestrator.go:108-111`, `orchestrator.go:523-527`
**Description**: When `os.MkdirAll` fails inside `createDebugDirectory`, the function returns `""`. The caller assigns this to `debugDir` without checking. Debug mode silently becomes a no-op when directory creation fails.

```go
debugDir := ""
if opts.Debug {
    debugDir = createDebugDirectory(workflowID, stateDir) // "" on failure, no warning
}
```

**Fix**: Return the error from `createDebugDirectory` and warn the user when debug mode can't be activated.

---

### H-06 · Hardcoded `/tmp` in Integration Tests
**Source**: SEC-001
**Files**: `tests/integration/integration_test.go:113,121,140,268,269`
**Description**: Several integration tests hardcode `/tmp/reset_counter.txt` and `/tmp/poll_counter.txt`. While there is a `runtime.GOOS != "windows"` guard, this is fragile and non-portable.

**Fix**: Replace `/tmp/...` with `filepath.Join(os.TempDir(), "...")`.

---

### H-07 · `code-structure.md` Entirely Describes Python Project
**Source**: DOC-009, DOC-013, DOC-016
**Files**: `docs/code-structure.md`
**Description**: The entire file describes the Python implementation: `src/` layout, `requirements.txt`, `pyproject.toml`, Docker PEP 668 pip installation, pytest commands, `.venv`. None of this applies to the Go project.

**Fix**: Rewrite `docs/code-structure.md` to describe the Go project (`cmd/`, `internal/`, `go.mod`, `go test`, etc.).

---

### H-08 · `WorkflowWaiting.ResetTime` Field Never Populated
**Source**: CODE-013
**Files**: `internal/events/events.go:36-44`, `internal/orchestrator/orchestrator.go:160-166`
**Description**: `WorkflowWaiting` has a `ResetTime time.Time` field. Every emission of this event leaves `ResetTime` as the zero value. Observers trying to display the reset time (e.g., "resets at 3pm") will see the epoch.

**Fix**: Populate `ResetTime` by computing `time.Now().Add(time.Duration(waitSec * float64(time.Second)))` when emitting `WorkflowWaiting`.

---

## MEDIUM

### M-01 · State File Location Wrong in `implementation-assumptions.md`
**Source**: DOC-008
**Files**: `docs/implementation-assumptions.md`, `internal/state/state.go`
**Description**: Docs say state files live in `.raymond/workflows/`. Code uses `.raymond/state/`.

**Fix**: Update the assumption document.

---

### M-02 · Debug Directory Naming Format Wrong in `debug-mode.md`
**Source**: DOC-005
**Files**: `docs/debug-mode.md:44-48`, `internal/orchestrator/orchestrator.go:523-524`
**Description**: Docs specify `.raymond/debug/{workflow_id}_{timestamp}/`. Code creates `.raymond/debug/{workflow_id}/` (no timestamp).

**Fix**: Either add a timestamp to the directory name or correct the documentation.

---

### M-03 · `--quiet` Note in `console-output.md` Says Flag Not Yet Added
**Source**: DOC-004
**Files**: `docs/console-output.md:288-290`
**Description**: Doc says "The `--quiet` flag needs to be added to the CLI argument parser." The flag is already implemented in `cli.go:83`.

**Fix**: Remove the stale note from the doc.

---

### M-04 · Duplicate State-Name Abbreviation Logic (3 Copies)
**Source**: CODE-008
**Files**: `internal/transitions/transitions.go:250-257`, `internal/executors/executors.go:86-97`
**Description**: Extension stripping (`.md`, `.sh`, `.bat`) is implemented separately in: `transitions.go` (for fork worker IDs), `executors.go` (`ExtractStateName`), and again inline elsewhere. The transitions copy also lowercases the result, creating inconsistency.

**Fix**: Consolidate into a single `ExtractStateName` function and call it from all sites.

---

### M-05 · Magic Strings for State Types Not Constants
**Source**: CODE-010, CODE-019
**Files**: `internal/events/events.go:52-58`, `internal/observers/console/console.go:142`, multiple executors
**Description**: The strings `"markdown"` and `"script"` appear as literal strings across many files. A typo creates a silent bug. The console observer uses `else` as the default for anything non-`"script"`, so an unknown type is silently treated as markdown.

**Fix**: Define `const StateTypeMarkdown = "markdown"` and `const StateTypeScript = "script"` in the `events` package and use them everywhere.

---

### M-06 · Temp File Cleanup Errors Swallowed in `WriteState`
**Source**: ERR-006, ERR-007
**Files**: `internal/state/state.go:156-165`
**Description**: When writing state fails, the cleanup `os.Remove(tmpName)` error is silently ignored. If temp-file removal fails, orphaned `.tmp` files accumulate in the state directory.

```go
if writeErr != nil {
    tmp.Close()
    os.Remove(tmpName)  // error ignored
    return fmt.Errorf("failed to write state: %w", writeErr)
}
if err := os.Rename(tmpName, final); err != nil {
    os.Remove(tmpName)  // error ignored
    return fmt.Errorf("failed to rename state file: %w", err)
}
```

**Fix**: Log (but don't return) the `os.Remove` error so orphaned temp files are visible.

---

### M-07 · `zipscope.FileExists` Returns `false` for All Errors
**Source**: CODE-015
**Files**: `internal/zipscope/zipscope.go:65-70`
**Description**: Any error (corrupted zip, permission denied) returns `false`, indistinguishable from "file not present." Callers cannot detect a broken archive.

**Fix**: Change signature to `(bool, error)` or at minimum return a distinct sentinel.

---

### M-08 · `AgentPaused` Reason Field Under-Specified
**Source**: CODE-011
**Files**: `internal/orchestrator/orchestrator.go:363-389`
**Description**: `AgentPaused.Reason` can be `"timeout"` or `"error"`. All non-timeout retryable errors (e.g., prompt file not found, Claude invocation failed) are reported as generic `"error"`. Observers can't provide useful detail.

**Fix**: Add `"prompt_error"`, `"claude_error"` etc. as distinct reasons, or include the error type in the event.

---

### M-09 · `zipscope` Lacks Independent Filename Validation
**Source**: SEC-002, SEC-011
**Files**: `internal/zipscope/zipscope.go:41-63`
**Description**: Path-traversal protection (`/`, `\` rejection) exists in `prompts.go` and `parsing.go` but not in `zipscope` itself. Any future call path that skips the upstream check would be unprotected.

**Fix**: Add the separator check at the top of `ReadText`, `FileExists`, and `ExtractScript`.

---

### M-10 · `SessionID` Pointer Semantics Inconsistent
**Source**: CODE-003
**Files**: `internal/state/state.go:60,72`, `internal/orchestrator/orchestrator.go:256-258`
**Description**: Script executor returns `ExecutionResult{SessionID: nil}`. The orchestrator then checks `if execResult.SessionID != nil` before updating `tr.Agent.SessionID`, which means a script step never updates the session. This is correct for scripts (they don't own sessions), but the pattern could silently lose a session if the executor contract is misunderstood.

**Fix**: Document the nil-means-"no change" contract explicitly in the interface comment, and add a test that verifies session ID is preserved through a script step.

---

### M-11 · `plan.md` Describes Goroutine-Per-Agent Concurrency Not Implemented
**Source**: ARCH-015
**Files**: `plan.md`, `internal/orchestrator/orchestrator.go:8-12`
**Description**: `plan.md` Phase 11 describes "one goroutine per active agent." The actual orchestrator is fully sequential (round-robin). The orchestrator itself says so in its own comments.

**Fix**: Update `plan.md` to reflect the sequential design, or implement true concurrent execution.

---

### M-12 · Budget Checked After Cost Accumulation (Allows Per-Invocation Overage)
**Source**: ARCH-007
**Files**: `internal/executors/markdown.go:219-232`
**Description**: Budget enforcement occurs after adding the invocation cost. A single expensive invocation can push spend well above the configured budget before the check triggers. Intended or not, this should be documented.

**Fix**: Add a comment clarifying this is by design (or add a pre-invocation budget check if strict enforcement is required).

---

### M-13 · `--verbose` Discussed in `console-output.md` Without Clear Semantics
**Source**: DOC-006
**Files**: `docs/console-output.md:291`
**Description**: Doc says verbose "will also enable DEBUG-level logging." This is not implemented (see H-01).

**Fix**: Update doc to match implementation (once H-01 is resolved).

---

### M-14 · `docs/code-structure.md` WSL/pytest/Docker Sections Entirely Wrong
**Source**: DOC-014, DOC-013, DOC-003
**Files**: `docs/code-structure.md`
**Description**: Sections on running tests (pytest), Docker (pip install with PEP 668), WSL integration, and Python package layout all describe the old Python codebase.

**Fix**: Covered by H-07 (rewrite the whole file).

---

### M-15 · State Persistence Only Tested on Limit-Error Path
**Source**: TEST-017
**Files**: `internal/orchestrator/orchestrator_test.go:299-340`
**Description**: The only test that verifies `WriteState` is called after each step exercises the `ClaudeCodeLimitError` path. Normal successful transitions are not tested for state persistence, leaving the atomic-write path largely uncovered in the orchestrator test.

**Fix**: Add a test that performs a successful `goto` transition and asserts that state was written to disk.

---

### M-16 · `TargetsMatch` Case Sensitivity Not Tested
**Source**: TEST-021
**Files**: `internal/policy/policy_test.go:349-378`
**Description**: `TargetsMatch("COUNT", "count.md")` (mixed case) is not covered. On case-insensitive filesystems (macOS, Windows) this matters.

**Fix**: Add tests with mixed-case state name vs file extension combinations.

---

## LOW

### L-01 · `ExecutionContext.Reporter` Field Always Nil/Unused
**Source**: CODE-004
**Files**: `internal/executors/executors.go:37`
**Description**: `Reporter any` is declared in `ExecutionContext` but never set or read anywhere. Dead field.

**Fix**: Remove the field.

---

### L-02 · `RunOptions.Width` Field Declared but Never Used
**Source**: ARCH-009
**Files**: `internal/orchestrator/orchestrator.go:68`
**Description**: `Width int` is in `RunOptions` and passed as `0` from CLI. Console observer accepts it but ignores it.

**Fix**: Remove or wire it (for adaptive terminal width in console output).

---

### L-03 · `README.md` Shows `pip install` Instead of Go Install
**Source**: DOC-018
**Files**: `README.md`
**Description**: Quick-start shows `pip install -e .` which is wrong for a Go project.

**Fix**: Replace with `go install ./cmd/raymond@latest` (or equivalent `go build` instructions).

---

### L-04 · `GetStateDir` Rewalks Directory Tree on Every Call
**Source**: CODE-016
**Files**: `internal/state/state.go:92-105`
**Description**: `FindRaymondDir` walks directories on every invocation. Called multiple times per CLI command.

**Fix**: Cache result after first call (or compute once and pass down).

---

### L-05 · `ExtractCostFromResults` Zero-Default Not Tested
**Source**: TEST-011
**Files**: `internal/executors/executors.go:146-163`
**Description**: No test for: empty results, missing `total_cost_usd` key, non-numeric value. These all return `0.0` silently.

**Fix**: Add edge-case unit tests.

---

### L-06 · `deepCopyAgent` Allocates Backing Array for Empty Stack
**Source**: CODE-017
**Files**: `internal/transitions/transitions.go:118-128`
**Description**: `make([]StackFrame, 0)` is allocated even when `len(a.Stack) == 0`. Minor inefficiency on the hot agent-copy path.

**Fix**: `if len(a.Stack) > 0 { newStack = make(...) }` — skip allocation when empty.

---

### L-07 · `docs/orchestration-design.md` References Python Asyncio
**Source**: DOC-001, DOC-002, DOC-019
**Files**: `docs/orchestration-design.md`
**Description**: Terminology table calls orchestrator "The running Python program… async." Code sections show `async def`, `asyncio.wait()`. Misleading for Go contributors.

**Fix**: Update the terminology table and remove/replace Python code samples.

---

### L-08 · `implementation-assumptions.md` References `src/config.py`
**Source**: DOC-007
**Files**: `docs/implementation-assumptions.md`
**Description**: Implementation notes reference `src/config.py` and `src/state.py`.

**Fix**: Update references to `internal/config/config.go` and `internal/state/state.go`.

---

### L-09 · Windows Process Group Cleanup Less Complete Than Unix
**Source**: ERR-019
**Files**: `internal/platform/platform_unix.go:40-49`, `internal/platform/platform_windows.go`
**Description**: Unix kills the entire process group (`-pid`). Windows uses default `Kill()` which may leave child processes (spawned by the script) orphaned.

**Fix**: On Windows, use a Job Object to track and terminate child processes, or document the limitation.

---

### L-10 · `TestMultipleHandlersAllCalled` Doesn't Verify Handler Order
**Source**: TEST-013
**Files**: `internal/bus/bus_test.go:39-52`
**Description**: Test asserts both handlers are called (counts=1) but handler execution order is snapshot-iteration order (map, non-deterministic). Not a correctness bug, but if ordering semantics ever matter this test won't catch regressions.

**Fix**: If ordering must be FIFO, add a sequence check; otherwise add a comment that ordering is intentionally undefined.

---

### L-11 · Parsing Tests Miss Empty Attribute Values
**Source**: TEST-016
**Files**: `internal/parsing/parsing_test.go`
**Description**: No test for `<fork return="">` (empty quoted attribute). Valid XML but behaviour is untested.

**Fix**: Add a test case.

---

## INFORMATIONAL

### I-01 · `docs/bash-states.md` and `docs/console-output.md` Have Minor Python Tone
**Source**: DOC-015
**Description**: Descriptions are principally correct but phrased in Python-centric language in a few places. Low priority to update.

### I-02 · `applyResult` Has Unused `fromState` Parameter
**Source**: CODE-020
**Files**: `internal/orchestrator/orchestrator.go:313`
**Description**: Parameter is blanked (`_ string`) with comment "kept for symmetry." Should either be used or removed.

### I-03 · `docs/sample-workflows.md` Commands Assume Binary on PATH
**Source**: DOC-012
**Description**: Example commands like `raymond workflows/...` assume `raymond` is installed. A note about building first would help new users.

### I-04 · PowerShell Support Intentionally Deferred
**Source**: ARCH-014
**Files**: `internal/platform/platform_windows.go:26`, `internal/observers/debug/debug.go:128`
**Description**: `.ps1` extension is recognized in the debug observer's `stripExt` but has no execution path. This is intentional per design docs.

---

## Summary Counts

| Severity  | Count |
|-----------|-------|
| Critical  | 4     |
| High      | 8     |
| Medium    | 16    |
| Low       | 11    |
| Info      | 4     |
| **Total** | **43** |

### Note on Test-Coverage Agent Results
The test-review agent reported several test files as "missing" (ccwrap, platform, cli, console, debug, titlebar, zipscope). These files DO exist (confirmed in initial exploration: ccwrap=629 lines, platform=457, cli=345, console=576, debug=252, titlebar=115, zipscope=225). The agent hit file-read limits. Legitimate test gaps are captured in M-15, M-16, L-05, L-11.
