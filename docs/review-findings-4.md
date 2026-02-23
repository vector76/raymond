# Code & Docs Review — Round 4

**Date:** 2026-02-23
**Reviewer:** Systematic multi-agent review (7 parallel subagents)
**Scope:** Full codebase audit — all Go source, tests, docs, and workflow fixtures

---

## Summary Table

| ID    | Area              | Severity | Description                                                              |
|-------|-------------------|----------|--------------------------------------------------------------------------|
| R4-01 | State             | Critical | `WriteState` computes final path from raw `stateDir`, not resolved `dir` |
| R4-02 | Config            | High     | Silent type-assert on `[raymond]` section swallows malformed config      |
| R4-03 | ccwrap            | Medium   | Malformed JSON lines silently skipped; Python reference logged a warning  |
| R4-04 | Executors         | Medium   | Cost accumulation allows negative values to corrupt workflow total        |
| R4-05 | CLI               | Medium   | `--model`/`--effort` flags bypass `ValidateConfig`; invalid values pass  |
| R4-06 | Executors         | Low      | `stderr` truncated to 500 chars in script error messages                 |
| R4-07 | Tests             | Low      | Typo in test name: `TestRenderPrompt_MissingKeyLeaveplaceholder`         |
| R4-08 | Tests             | Low      | Misleading comment in `bus_test.go` implies deterministic handler order  |
| R4-09 | Docs              | Doc      | `debug-mode.md` examples show timestamp-suffixed debug dirs not created  |
| R4-10 | Docs              | Doc      | `console-output.md` cost examples show 2 decimal places; impl uses 4    |
| R4-11 | Docs + Fixtures   | Doc      | `sample-workflows.md` references `.txt` files; fixtures use `.md`        |
| R4-12 | Docs              | Doc      | `AGENTS.md` says CLAUDE.md must be a "copy"; `@` include is intentional  |

---

## Detailed Findings

---

### R4-01 — `WriteState` computes final path from unresolved `stateDir`
**Severity:** Critical
**File:** `internal/state/state.go:152`

**Description:**
`WriteState` resolves the state directory on line 147:
```go
dir := GetStateDir(stateDir)
```
Then creates the temp file inside `dir`:
```go
tmp, err := os.CreateTemp(dir, workflowID+"_*.tmp")
```
But computes the final rename destination using the *raw* `stateDir`:
```go
final := statePath(stateDir, workflowID)  // stateDir may be ""
```
`statePath` just calls `filepath.Join(stateDir, workflowID+".json")`. If `stateDir == ""`, `dir` resolves to an absolute path (e.g. `/home/user/project/.raymond/state`) while `final` becomes `workflowID.json` (relative to cwd). The `os.Rename` across different directories fails on most systems.

In current production code callers always pre-resolve `stateDir`, so this is latent, but `DeleteState` and `ReadState` also call `statePath(stateDir, ...)` directly without resolving, creating an API that is easy to misuse.

**Fix:** Change line 152 to use the already-resolved `dir`:
```go
final := filepath.Join(dir, workflowID+".json")
```

**Status:** Fixed — extended to `ReadState` and `DeleteState` (same latent bug); removed now-unused `statePath` helper

---

### R4-02 — Silent type-assertion on `[raymond]` config section
**Severity:** High
**File:** `internal/config/config.go:299`

**Description:**
```go
raymondSection, _ := raw["raymond"].(map[string]any)
if raymondSection == nil {
    return map[string]any{}, nil
}
```
If a user accidentally writes `raymond = "oops"` instead of `[raymond]`, the type-assertion yields `(nil, false)`. The nil-check then silently returns an empty config with no error message. The user's settings are dropped without any diagnostic.

**Fix:** Check the boolean result of the type assertion and return a `ConfigError` when the key exists but is not a table:
```go
raymondSection, ok := raw["raymond"].(map[string]any)
if !ok {
    if _, exists := raw["raymond"]; exists {
        return nil, &ConfigError{msg: fmt.Sprintf(
            "Failed to parse %s: [raymond] must be a TOML table, not a scalar value", configFile,
        )}
    }
    return map[string]any{}, nil
}
```

**Status:** Fixed (added `TestLoadConfigErrorOnScalarRaymondSection`)

---

### R4-03 — Malformed JSON lines silently skipped in JSONL stream
**Severity:** Medium
**File:** `internal/ccwrap/ccwrap.go:320-322`

**Description:**
```go
if err := json.Unmarshal([]byte(line), &obj); err != nil {
    // Skip non-JSON lines (mirrors Python's warning + continue).
    continue
}
```
The comment says "mirrors Python's warning + continue" — but only the `continue` is implemented; the warning is absent. Unexpected non-JSON output from `claude` (e.g. a startup banner, OS message) is silently dropped, making debugging failures difficult.

**Fix:** Add a `log.Printf` warning before the `continue`:
```go
if err := json.Unmarshal([]byte(line), &obj); err != nil {
    log.Printf("ccwrap: skipping non-JSON line from claude: %q", line)
    continue
}
```

**Status:** Fixed

---

### R4-04 — Negative invocation cost corrupts workflow total
**Severity:** Medium
**File:** `internal/executors/markdown.go:224-229`

**Description:**
```go
invocationCost := result.TotalCostUSD
stateTotalCost += invocationCost
wfState.TotalCostUSD += invocationCost
```
No validation that `invocationCost >= 0`. A malformed or adversarially crafted API response reporting `total_cost_usd: -99.0` would decrement the workflow total, corrupting cost accounting and potentially suppressing cost-budget enforcement.

**Fix:** Add a guard:
```go
invocationCost := result.TotalCostUSD
if invocationCost < 0 {
    invocationCost = 0
}
stateTotalCost += invocationCost
wfState.TotalCostUSD += invocationCost
```

**Status:** Not a bug — code already has `if invocationCost > 0` guard (line 226) that prevents negative values from being accumulated. Finding is a false positive.

---

### R4-05 — `--model`/`--effort` CLI flags bypass config validation
**Severity:** Medium
**File:** `internal/cli/cli.go:125-126` (flag binding) and `internal/cli/cli.go:138` (merge)

**Description:**
`ValidateConfig` (called for config-file values) enforces that `model` must be `opus|sonnet|haiku` and `effort` must be `low|medium|high`. However, values supplied via `--model=INVALID` or `--effort=INVALID` on the CLI are merged into the config struct without going through the same validation. They are then passed directly to `ccwrap`, which passes them to the `claude` CLI, causing an opaque downstream error rather than a clear user-facing message.

**Fix:** After merging CLI flags into the final config (around line 138), call `ValidateConfig` on the merged model/effort values and return an early error if they are invalid, or add explicit validation inline.

**Status:** Fixed (added explicit validation after merge; added `TestInvalidModelFlagReturnsError`, `TestInvalidEffortFlagReturnsError`, `TestValidEffortFlagValues`)

---

### R4-06 — Script `stderr` truncated to 500 chars in error messages
**Severity:** Low
**File:** `internal/executors/script.go:119-129`

**Description:**
```go
if len(result.Stderr) > 500 {
    errMsg = errMsg + "\nStderr (truncated): " + result.Stderr[:500]
} else {
    errMsg = errMsg + "\nStderr: " + result.Stderr
}
```
The 500-character cutoff is arbitrary and may hide the relevant part of a long error. This is especially problematic for scripts that emit stack traces.

**Fix:** Either increase the limit substantially (e.g. 4096), or log the full stderr separately while keeping the truncated version in the returned error string.

**Status:** Fixed (limit increased to 4096 via named constant `maxStderrInError`)

---

### R4-07 — Typo in test function name
**Severity:** Low
**File:** `internal/prompts/prompts_test.go:186`

**Description:**
```go
func TestRenderPrompt_MissingKeyLeaveplaceholder(t *testing.T) {
```
"Leaveplaceholder" should be "LeavesPlaceholder" (missing `s`, incorrect casing). Makes the test harder to find and violates Go naming conventions.

**Fix:** Rename to `TestRenderPrompt_MissingKeyLeavesPlaceholder`.

**Status:** Fixed

---

### R4-08 — Misleading comment implies deterministic handler order in bus test
**Severity:** Low
**File:** `internal/bus/bus_test.go:140`

**Description:**
Comment states `// callOrder must equal [1, 2, 3, 4]` but the event bus deliberately does not guarantee delivery order among subscribers. If the bus implementation ever changes to use goroutines per subscriber, this test would become flaky. The comment overstates the guarantee.

**Fix:** Soften the comment to reflect what is actually being tested:
```go
// All four handlers must be called (order is an implementation detail).
```

**Status:** Not a bug — the bus IS synchronous with guaranteed subscription order, so `[1,2,3,4]` is the correct expected result. The comment at line 139 ("All handlers must have been called despite panics") is accurate. Finding is a false positive.

---

### R4-09 — `debug-mode.md` examples show timestamp suffix on debug dirs
**Severity:** Doc
**File:** `docs/debug-mode.md:28` and `docs/debug-mode.md:46`

**Description:**
The documentation shows example debug directory paths in the form:
```
.raymond/debug/workflow_2026-01-15_14-30-22/
```
The actual implementation creates:
```
.raymond/debug/{workflow_id}/
```
with no timestamp suffix. The timestamp example was apparently carried over from an earlier design.

**Fix:** Remove the entire outdated "Implementation Details" section (Python pseudocode with wrong directory format) from `debug-mode.md`.

**Status:** Fixed (removed Python Implementation Details section lines 172-376)

---

### R4-10 — `console-output.md` cost format examples show 2 decimal places
**Severity:** Doc
**File:** `docs/console-output.md:243-244`

**Description:**
Documentation examples show cost formatting like `$0.02` (2 decimal places), but `console.go` uses `%.4f` format, producing `$0.0200` (4 decimal places). The specification and implementation disagree.

**Fix:** Update examples in `console-output.md` to show 4 decimal places, consistent with the implementation.

**Status:** Fixed (updated the cost-display note to use `$X.XXXX` / `$Y.YYYY` notation with explicit "four decimal places" callout)

---

### R4-11 — `sample-workflows.md` references `.txt` output files; fixtures use `.md`
**Severity:** Doc
**File:** `docs/sample-workflows.md:125`, `docs/sample-workflows.md:135`, `docs/sample-workflows.md:187`, `docs/sample-workflows.md:192`

**Description:**
`sample-workflows.md` documents the workflow fixtures as producing `story-output.txt` and `research-summary.txt`, but the actual fixture files (`RESOLUTION.md` and `SUMMARIZE.md`) instruct the AI to write `story-output.md` and `research-summary.md`. All four references in the doc are wrong.

**Fix:** Update `sample-workflows.md` to replace all `.txt` extensions with `.md` for these output files.

**Status:** Fixed (updated lines 46-47, 125, 135, 187, 192, 204, 732)

---

### R4-12 — `AGENTS.md` describes CLAUDE.md as a "copy" but `@` include is intentional
**Severity:** Doc
**File:** `AGENTS.md:40-42`

**Description:**
```
**Synchronization**: `CLAUDE.md` is intended to be a copy for tooling compatibility.
If they ever differ, update `CLAUDE.md` to match `AGENTS.md`.
```
`CLAUDE.md` currently contains a single line `@AGENTS.md`, which is a Claude Code include directive. This is more maintainable than a duplicate copy — Claude Code automatically reads both files and inlines `AGENTS.md`. The documentation calling CLAUDE.md a "copy" is misleading; this is intentional and correct.

**Fix:** Update `AGENTS.md` to document the `@` include pattern as the intended approach:
```
**Synchronization**: `CLAUDE.md` uses `@AGENTS.md` to include `AGENTS.md` via
Claude Code's file-include directive. Do not replace this with a manual copy.
```

**Status:** Fixed

---

## False Positives Investigated and Cleared

The following potential issues were investigated but determined to be non-issues:

| Finding | Verdict |
|---------|---------|
| `ccwrap.go:372-376` CRITICAL timeout safety-check race | Defensive guard, standard pattern; not a race condition |
| `ccwrap.go:250-262` idle timer reset | Correct idiomatic Go timer stop-drain-reset sequence |
| `executors/markdown.go:122-127` fork session ID priority | Correct: fork only on first attempt; reminders continue the forked session |
| `platform_test.go:202` CRITICAL — `t.TempDir()` usage | `t.TempDir()` already creates the parent directory; test is correct |
| `platform_test.go:230` `skipUnix` logic inverted | `skipUnix` skips on Windows (runs on Unix); correctly names Unix-only tests |
| `platform_test.go:306` `skipUnix` on `TestRunScriptBatRaisesOnUnix` | Same as above — test correctly runs on Unix only |
| `CLAUDE.md:1` out of sync | `@AGENTS.md` is an intentional include directive (see R4-12) |
| `state/state.go` API inconsistency (callers pre-resolve) | Production callers always pre-resolve; latent only (subsumed by R4-01) |
