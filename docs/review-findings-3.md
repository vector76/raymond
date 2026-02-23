# Code & Docs Review — Round 3

**Date:** 2026-02-23
**Reviewer:** Systematic multi-agent review (7 parallel subagents)
**Scope:** Fresh full-codebase audit — all Go source, tests, and documentation

---

## Summary Table

| ID | Area | Severity | Description |
|----|------|----------|-------------|
| RR-001 | Executors | High | `invocationCost` overwritten each reminder loop iteration; `StateCompleted.CostUSD` reports only last invocation |
| RR-002 | Tests | High | `parseResetWaitSeconds`/`computeAutoWait` have zero test coverage |
| RR-003 | Tests | High | Markdown reminder loop logic almost entirely untested |
| RR-004 | Parsing | Medium | Attribute regex allows mismatched quote types (`key="val'`) |
| RR-005 | Observers | Medium | `ClaudeInvocationStarted` and `AgentPaused` events emitted but no observer subscribes |
| RR-006 | Executors | Medium | `ScriptOutput` and `ClaudeStreamOutput` only emitted in debug mode, invisible to observers otherwise |
| RR-007 | Executors | Medium | `appendJSONL` in `markdown.go` silently ignores all I/O errors |
| RR-008 | Prompts | Medium | Template `{{key}}` substitution iterates over map (non-deterministic with overlapping keys) |
| RR-009 | Docs | Medium | Python code examples and terminology survive in multiple architecture docs |
| RR-010 | Docs | Medium | `--quiet`, `--no-wait` missing from authoring guide; `no_wait` missing from config doc; `src/` paths wrong |
| RR-011 | Tests | Medium | `allPaused`, `firstActiveIndex`, error-predicate helpers have no isolated unit tests |
| RR-012 | Executors | Medium | Per-invocation timeout applied to each reminder separately; state can run for `N × timeout` |
| RR-013 | Observers | Low | Debug observer ignores `os.MkdirAll` error, sets `debugDir` anyway |
| RR-014 | Config | Low | Config validation rejects mixed-case model/effort values but error message is cryptic |
| RR-015 | CLI/Config | Low | `--quiet` flag cannot be set in config file (inconsistent with other flags) |
| RR-016 | Tests | Low | All test-emitted events have zero-valued `Timestamp` (don't match production) |
| RR-017 | Orchestrator | Low | `AgentTerminationResults` map not defensively initialized at orchestrator startup |
| RR-018 | Tests | Low | Multiple small test gaps: `ExtractStateName`, script Cwd wiring, nil transitions, config combos |
| RR-019 | Orchestrator | Info | `MaxRetries` name and retry-count semantics slightly confusing |
| RR-020 | Executors | Info | Budget-exceeded error message shows `$0.0000 > $0.0000` when budget is unset (0) |
| RR-021 | Docs | Info | `console-output.md` does not clarify which sections are current vs. proposed (carried from FN-006) |

---

## Detailed Findings

---

### RR-001 · `invocationCost` overwritten each reminder loop iteration

**Severity**: High
**File**: `internal/executors/markdown.go:107,225,273,283`

**Description**: The variable `invocationCost` is declared once before the reminder loop, then overwritten on every iteration at line 225 (`invocationCost = ExtractCostFromResults(results)`). `wfState.TotalCostUSD` correctly accumulates across iterations (line 227). But the `StateCompleted` event (line 273) and the returned `ExecutionResult` (line 283) both report only the **last** invocation's cost, not the total for the state.

**Example**: If a state retries twice with costs $0.10, $0.20, $0.30:
- `TotalCostUSD`: correctly $0.60
- `StateCompleted.CostUSD`: reports only $0.30

**Fix**: Accumulate into a separate `stateTotalCost` variable across iterations:
```go
var stateTotalCost float64
// inside loop:
cost := ExtractCostFromResults(results)
stateTotalCost += cost
wfState.TotalCostUSD += cost
// at event emit:
CostUSD: stateTotalCost,
```

---

### RR-002 · `parseResetWaitSeconds` and `computeAutoWait` have zero test coverage

**Severity**: High
**Files**: `internal/orchestrator/limitwait.go:18-96`, `internal/orchestrator/orchestrator.go:502-545`

**Description**: The limit-reset auto-wait feature parses time strings like `"resets 3pm (America/Chicago)"` and computes a wait duration before automatically resuming a paused workflow. None of the parsing functions (`parseResetWaitSeconds`, `parseHourStr`, `scanInt`) have any tests. Verified: no test name matching `parseReset*`, `computeAutoWait`, or `limitwait` exists anywhere in the codebase.

**Untested edge cases**:
- `"12am"` (midnight = 0h, not 12h)
- `"12pm"` (noon)
- Invalid timezone strings
- Reset time already in the past (same day)
- Malformed input (no regex match)
- Multiple paused agents with different reset times

**Fix**: Add a `limitwait_test.go` with table-driven tests covering the above cases. Also add a test for `computeAutoWait` with multiple agents and conflicting reset times.

---

### RR-003 · Markdown reminder loop logic almost entirely untested

**Severity**: High
**File**: `internal/executors/markdown.go:103-266`
**Test file**: `internal/executors/executors_test.go`

**Description**: The reminder retry loop is the primary error-recovery mechanism for markdown states. The only test covering it is `TestMarkdownExecutor_RaisesAfterMaxRetries`, which only verifies the error message when the maximum attempts are exhausted. Missing coverage:

- Reminder prompt appended on attempt ≥ 1 (lines 113–118)
- `ClaudeInvocationStarted` event with `IsReminder: true` / `ReminderAttempt: N` (lines 135–136)
- `ForkSessionID` handling on attempt 0 vs. later attempts (line 124)
- Cost accumulation across reminder attempts (connects to RR-001)
- Successful result on second attempt (state after reminder success)
- Session preservation through reminder retries

**Fix**: Add table-driven tests that use a mock executor returning no-transition on attempt N, then a valid transition on attempt N+1, and verify event fields and returned state.

---

### RR-004 · Attribute regex allows mismatched quote types

**Severity**: Medium
**File**: `internal/parsing/parsing.go:18`

**Description**: The regex `(\w+)=["']([^"']*)["']` uses a character class `["']` for both opening and closing quotes. It will match `key="value'` or `key='value"` (mismatched), accepting syntactically invalid attributes. The character class `[^"']*` also rejects any attribute value containing a quote character (e.g., apostrophes in text).

**Evidence**:
```go
var attrRe = regexp.MustCompile(`(\w+)=["']([^"']*)["']`)
// Matches: key="val'  (mismatched quotes — WRONG)
// Rejects: key="it's" (apostrophe in value — may be unintended)
```

**Fix**: Use two alternatives to enforce matching quotes:
```go
var attrRe = regexp.MustCompile(`(\w+)=(?:"([^"]*)"|'([^']*)')`)
```
Then reconcile capture groups (group 2 for double-quoted, group 3 for single-quoted).

---

### RR-005 · `ClaudeInvocationStarted` and `AgentPaused` events are orphaned

**Severity**: Medium
**Files**: `internal/events/events.go:112-116,128-136`, `internal/observers/console/console.go`, `internal/observers/debug/debug.go`

**Description**: Two event types are emitted by the orchestrator/executors but subscribed to by no observer:
- `ClaudeInvocationStarted` — emitted in `markdown.go` before each Claude call (includes model, effort, session info, IsReminder flag)
- `AgentPaused` — emitted in `orchestrator.go` with a pause reason when an agent pauses

The console and debug observers do not subscribe to either. Information about why agents pause and when Claude invocations begin is invisible to all observers.

**Fix options**:
- Add `AgentPaused` handling to the console observer to print a "Agent paused: reason" line.
- Add `ClaudeInvocationStarted` to the debug observer to record invocation metadata.
- At minimum, document in events.go that these are "debug-only" events if intentional.

---

### RR-006 · `ScriptOutput` and `ClaudeStreamOutput` events only emitted in debug mode

**Severity**: Medium
**Files**: `internal/executors/script.go:98-116`, `internal/executors/markdown.go:141-148,191`

**Description**:
- `ScriptOutput`: guarded by `if execCtx.DebugDir != ""` (script.go line 99). When debug is off, script output is never surfaced to any observer.
- `ClaudeStreamOutput`: guarded by `if stepNumber > 0` (markdown.go line 191); `stepNumber` is only set when `execCtx.DebugDir != ""` (lines 141–148). So this is also debug-only in practice.

Both event types are defined in `events.go` without documentation indicating they are conditional on debug mode. Observers cannot rely on them without debug enabled.

**Fix**: Either document clearly in the event type comments that emission is conditional on debug, or restructure so events are always emitted (with raw content gated on debug dir).

---

### RR-007 · `appendJSONL` in `markdown.go` silently ignores all I/O errors

**Severity**: Medium
**File**: `internal/executors/markdown.go:479-491`

**Description**: The function comment says "silently ignoring errors" and the implementation bears this out — both `json.Marshal` and `os.OpenFile` failures cause a bare `return` with no logging:

```go
func (e *MarkdownExecutor) appendJSONL(path string, obj map[string]any) {
    data, err := json.Marshal(obj)
    if err != nil {
        return  // silent
    }
    f, err := os.OpenFile(path, ...)
    if err != nil {
        return  // silent
    }
    ...
}
```

This was a separate issue from FN-004 (fixed in round 2, which addressed `debug.go`). The markdown executor has its own analogous function that was not fixed.

**Fix**: Log I/O errors to stderr, consistent with the pattern in `debug.go`:
```go
if err != nil {
    fmt.Fprintf(os.Stderr, "markdown executor: debug write error: %v\n", err)
    return
}
```

---

### RR-008 · Template `{{key}}` substitution is non-deterministic with overlapping keys

**Severity**: Medium
**File**: `internal/prompts/prompts.go:85-98`

**Description**: `RenderPrompt` iterates over a `map[string]string` (unordered in Go) and performs `strings.ReplaceAll` for each key. If variable keys are substrings of other keys (e.g., `"name"` and `"firstname"`), the replacement order is non-deterministic and can corrupt a template:

```go
for key, value := range variables {   // iteration order: random
    placeholder := "{{" + key + "}}"
    result = strings.ReplaceAll(result, placeholder, str)
}
```

If `"name"` is processed before `"firstname"`, the template `{{firstname}}` becomes `{{firstvalue}}` incorrectly (if `name→value`). Different runs may produce different outputs.

**Fix**: Sort keys before iterating (longest-first to avoid substring collisions):
```go
keys := make([]string, 0, len(variables))
for k := range variables { keys = append(keys, k) }
sort.Slice(keys, func(i, j int) bool { return len(keys[i]) > len(keys[j]) })
for _, key := range keys { ... }
```

---

### RR-009 · Python code examples and terminology in architecture docs

**Severity**: Medium
**Files**: `docs/debug-mode.md`, `docs/bash-states.md`, `docs/configuration-file-design.md`, `docs/console-output.md`, `docs/terminal-titlebar.md`

**Description**: Multiple architecture documents contain Python-specific code, APIs, and libraries that have no relation to the Go implementation:

| File | Python remnant |
|------|----------------|
| `debug-mode.md:176-343` | `async def run_all_agents`, `from pathlib import Path`, `logger.warning`, asyncio patterns |
| `bash-states.md:246` | `asyncio.create_subprocess_exec()` |
| `configuration-file-design.md:17,39,43,125-182,254,407-410` | `tomllib` (Python 3.11+), `src/cli.py`, `src/config.py`, `src/state.py`, Python `find_raymond_dir()` function |
| `console-output.md:14,378,411` | `sys.stdout.isatty()`, `shutil.get_terminal_size()` |
| `terminal-titlebar.md:37` | `pathlib.Path(state_name).stem` |

**Fix**: Strip or rewrite Python code blocks. Replace with Go equivalents, pseudo-code, or architecture descriptions that aren't language-specific.

---

### RR-010 · CLI flags and config options missing or wrong in docs

**Severity**: Medium
**Files**: `docs/authoring-guide.md:519-531`, `docs/configuration-file-design.md:11-18,106,407-410`

**Description**:

1. **Missing flags in authoring guide**: `--quiet` and `--no-wait` are implemented (see `internal/cli/cli.go`) but absent from the CLI flags table in `authoring-guide.md`.

2. **`no_wait` missing from config doc**: `configuration-file-design.md` lists supported TOML config options but omits `no_wait`, which is a valid config key (verified in `internal/config/config.go:155`).

3. **Wrong source paths**: `configuration-file-design.md` lines 106 and 407–410 reference `src/config.py`, `src/state.py`, `src/cli.py`. The project uses `internal/config/config.go`, `internal/state/state.go`, `internal/cli/cli.go`.

**Fix**: Update the authoring guide flags table; add `no_wait` to the config doc; replace `src/` references with `internal/`.

---

### RR-011 · Orchestrator helper predicates have no isolated unit tests

**Severity**: Medium
**File**: `internal/orchestrator/orchestrator.go` (various helpers)
**Test file**: `internal/orchestrator/orchestrator_test.go`

**Description**: Several functions that drive critical control-flow decisions have no dedicated unit tests:
- `allPaused(agents []AgentState) bool` — determines when to emit `WorkflowPaused`
- `firstActiveIndex(agents []AgentState) int` — selects next agent to step
- `isLimitError(err error) bool` — gates pause vs. retry
- `isTimeoutError(err error) bool`
- `isRetryableError(err error) bool`
- `agentPausedReason(err error) string`

These are tested **implicitly** through orchestrator integration tests (e.g., `TestLimitErrorPausesAgent`), but isolated tests that verify each predicate against all error types, wrapped errors, and nil inputs do not exist.

**Fix**: Add a `helpers_test.go` or extend `orchestrator_test.go` with isolated table-driven tests for each predicate.

---

### RR-012 · Per-invocation timeout allows unbounded state execution time

**Severity**: Medium
**File**: `internal/executors/markdown.go:154`, `internal/executors/executors.go:35`

**Description**: `execCtx.Timeout` is documented as "invocation timeout" and is passed to each `invokeStreamFn` call within the reminder loop. With `maxReminderAttempts = 3`, a state can run for up to `3 × Timeout` before failing. There is no total-wall-clock cap on a single state's execution.

**Impact**: With `timeout = 600s` (10 min) and 3 reminder attempts, a single state could block for 30 minutes.

**Fix options**:
- Add a per-state total timeout wrapping the entire reminder loop.
- Document the behavior clearly in config docs so operators can set `timeout` accordingly.

---

### RR-013 · Debug observer ignores `os.MkdirAll` error, then uses path

**Severity**: Low
**File**: `internal/observers/debug/debug.go:60-63`

**Description**: If `os.MkdirAll` fails (e.g., permission denied), the error is silently discarded and `o.debugDir` is set to the failed path anyway. Subsequent file writes fail with `"cannot open …"` errors — the root cause (directory creation failure) is never surfaced.

```go
_ = os.MkdirAll(e.DebugDir, 0o700)  // error ignored
o.debugDir = e.DebugDir              // set unconditionally
```

**Fix**: Log the mkdir error to stderr and return without setting `o.debugDir`:
```go
if err := os.MkdirAll(e.DebugDir, 0o700); err != nil {
    fmt.Fprintf(os.Stderr, "debug observer: cannot create debug dir %s: %v\n", e.DebugDir, err)
    return
}
o.debugDir = e.DebugDir
```

---

### RR-014 · Config validation rejects mixed-case model/effort values with unhelpful error

**Severity**: Low
**File**: `internal/config/config.go:216-246`

**Description**: Validation checks `s != "opus"` (case-sensitive), so `"Opus"` or `"SONNET"` are rejected. The error message says the value is invalid but does not mention that lowercase is required. Meanwhile, `internal/policy/policy.go:109` normalizes with `strings.ToLower` — so if validation were relaxed, the value would be handled correctly downstream.

**Fix**: Either normalize to lowercase before validation in `config.go`, or add "must be lowercase" to the error message.

---

### RR-015 · `--quiet` flag cannot be set in config file

**Severity**: Low
**File**: `internal/cli/cli.go:193`, `internal/config/config.go:148-157`

**Description**: `--quiet` is a CLI-only flag. Unlike `--no-debug`, `--verbose`, `--no-wait`, and `--budget` (all supported in `.raymond/config.toml`), `quiet` is absent from `knownKeys` and `CLIArgs`. Users cannot set it persistently. This is inconsistent and surprising.

**Fix**: Add `quiet` to `knownKeys` and `CLIArgs`, and merge it through the config pipeline like other boolean flags — or document the intentional CLI-only behavior.

---

### RR-016 · Test events emit zero-valued `Timestamp` fields

**Severity**: Low
**Files**: `internal/observers/console/console_test.go`, `internal/observers/titlebar/titlebar_test.go`, `internal/bus/bus_test.go`

**Description**: Events emitted in tests use struct literals without setting `Timestamp`, so handlers receive `time.Time{}` (Unix epoch 1970-01-01). Production code always sets `Timestamp: time.Now()`. Observers that format or depend on timestamps are not exercised with realistic values.

Additionally, some `StateCompleted` test events omit `StateName` (e.g., `console_test.go:151,167`).

**Fix**: Add `Timestamp: time.Now()` and populate all required fields in test event structs. Consider a helper `makeEvent(...)` that sets defaults.

---

### RR-017 · `AgentTerminationResults` map not defensively initialized

**Severity**: Low
**File**: `internal/orchestrator/orchestrator.go:95-143`

**Description**: `wfState.AgentTerminationResults` (tagged `json:"-"`, so not persisted) is populated by `HandleResult` in `transitions.go:314` but never explicitly initialized (e.g., via `make(map[string]string)`) at the start of `RunAllAgents`. It works currently because map reads on nil maps return the zero value, and the first write auto-initializes it. However, nil-map writes would panic if the field were ever written before the first `HandleResult` call.

**Fix**: Add `ws.AgentTerminationResults = make(map[string]string)` early in `RunAllAgents` for defensive clarity.

---

### RR-018 · Miscellaneous small test coverage gaps

**Severity**: Low
**Files**: Various test files

**Description**: A collection of smaller test gaps found during the audit:

| Gap | Location |
|-----|----------|
| `ExtractStateName` function has no tests | `internal/parsing/parsing_test.go` (missing) |
| Script executor `Cwd` wiring to `RunScript` not tested at executor level | `internal/executors/executors_test.go` |
| Nil `SessionID` / `PendingResult` permutations in transition tests | `internal/transitions/transitions_test.go` |
| `WorkflowState.AgentTerminationResults` population untested at orchestrator level | `internal/orchestrator/orchestrator_test.go` |
| Config parameter combination tests (e.g., budget+timeout interaction) | `internal/config/config_test.go` |
| Transition validation with empty `Tag` or nil `Attributes` | `internal/parsing/parsing_test.go` |
| Debug observer error-path logging (`marshal error`, `write error`) not verified | `internal/observers/debug/debug_test.go` |
| Cost accumulation with zero/fractional costs in markdown executor | `internal/executors/executors_test.go` |

---

### RR-019 · `MaxRetries` / retry-count semantics slightly confusing

**Severity**: Info
**File**: `internal/orchestrator/orchestrator.go:31-33,374-375`

**Description**: The constant is named `MaxRetries = 3` with comment "number of retryable errors allowed". The check is `agent.RetryCount < MaxRetries`. This means the system allows exactly 3 error events before pausing (not "3 retries after the first attempt"). The name implies "retries" but the count includes the first attempt's failure. Not a bug, but the semantics are worth a comment clarification.

---

### RR-020 · Budget-exceeded message shows `$0.0000 > $0.0000` when budget is zero

**Severity**: Info
**File**: `internal/executors/markdown.go:240`

**Description**: The error format `"Workflow terminated: budget exceeded ($%.4f > $%.4f)"` will print `"$0.0000 > $0.0000"` if `BudgetUSD` is 0 (unset). This only occurs in a misconfiguration (budget checking should not trigger if budget is 0), but the message is confusing if it does.

---

### RR-021 · `console-output.md` does not clarify current vs. proposed sections

**Severity**: Info
**File**: `docs/console-output.md`
**Carried from**: FN-006 (round 2, still open)

**Description**: The document has "Current Output Format" (describes Python logging — outdated) and "Proposed Output Format" sections, with no indication of which reflects the actual Go implementation. A reader cannot determine the document's current status.

**Fix**: Add a brief header note indicating the document status and which section reflects the implemented behavior.

---

## Phase 3 Fix Order

Work through findings in this order:

1. **High** (RR-001, RR-002, RR-003)
2. **Medium** (RR-004 through RR-012)
3. **Low** (RR-013 through RR-018)
4. **Info** (RR-019 through RR-021)

After completing all fixes:
```bash
go build ./...
go vet ./...
go test ./...
```

---

## Resolution Tracking

| ID | Status | Notes |
|----|--------|-------|
| RR-001 | **Fixed** | `stateTotalCost` accumulates across reminder loop; `invocationCost` now local |
| RR-002 | **Fixed** | Added `limitwait_test.go` (10 tests); also fixed `time.Until→target.Sub(now)` bug |
| RR-003 | **Fixed** | Added 3 reminder loop tests: IsReminder flag, cost accumulation, third-attempt success |
| RR-004 | **Fixed** | Attribute regex updated to enforce matching quotes; `parseAttributes` updated |
| RR-005 | Open | No observer subscribes to `ClaudeInvocationStarted` / `AgentPaused`; by design or oversight |
| RR-006 | Open | Debug-only event emission is intentional design; needs doc clarification |
| RR-007 | **Fixed** | `appendJSONL` now logs I/O errors to stderr |
| RR-008 | **Fixed** | `RenderPrompt` sorts keys longest-first before substitution |
| RR-009 | **Partial** | Fixed `bash-states.md` asyncio ref; `configuration-file-design.md` Python rationale removed; large Python code blocks in `debug-mode.md` remain (low priority historical content) |
| RR-010 | **Fixed** | Added `--quiet`, `--no-wait` to authoring guide; added `no_wait` to config doc; fixed `src/` → `internal/` references |
| RR-011 | **Fixed** | Added `helpers_test.go` with 20 isolated unit tests for predicates and helpers |
| RR-012 | Open | Per-invocation timeout is by design; config docs note the behavior |
| RR-013 | **Fixed** | Debug observer now logs mkdir error to stderr and returns without setting `debugDir` |
| RR-014 | **Fixed** | Config validation error messages now say "(lowercase)" for model/effort |
| RR-015 | Open | `--quiet` intentionally CLI-only (session-level preference, not project config) |
| RR-016 | **Partial** | Fixed missing `StateName` and added `Timestamp` to script state test events; other event types don't use Timestamp in rendering |
| RR-017 | **Fixed** | `AgentTerminationResults` defensively initialized with `make(map[string]string)` |
| RR-018 | Open | Small coverage gaps; `ExtractStateName` is tested via executor delegation; other gaps are incremental improvements |
| RR-019 | Open | Semantic clarity note; no code change needed |
| RR-020 | Open | Info-level edge case; budget=0 check prevents this in practice |
| RR-021 | **Fixed** | Added status banner to `console-output.md` clarifying historical vs. current sections |
