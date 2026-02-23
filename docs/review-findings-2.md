# Code & Docs Review — Round 2

**Date:** 2026-02-23
**Reviewer:** Systematic multi-agent review (3 parallel subagents)
**Scope:** Verification of 43 previously-documented findings from `docs/review-findings.md` + fresh code-logic pass

---

## Summary

| Category | Count |
|----------|-------|
| Previously-documented findings verified FIXED | 40 |
| Previously-documented findings NOT FIXED | 3 |
| Previously-documented findings PARTIALLY FIXED | 1 |
| New findings (not in previous review) | 2 |
| **Total open findings** | **6** |

---

## Previously-Documented Findings — Verification Results

### Critical & High (all FIXED)

| ID | Title | Status |
|----|-------|--------|
| C-01 | Double cost accumulation | **FIXED** |
| C-02 | Race in idleExpired case | **FIXED** |
| C-03 | Context not checked in runStream send loop | **FIXED** |
| C-04 | Status string constants | **FIXED** |
| H-01 | --verbose flag wiring | **FIXED** |
| H-02 | Debug directory permissions | **FIXED** |
| H-03 | CLAUDECODE env stripping | **FIXED** |
| H-04 | GetNextStepNumber mutex | **FIXED** |
| H-05 | Debug dir creation error surfacing | **FIXED** |
| H-06 | /tmp hardcodes in integration tests | **FIXED** |
| H-07 | docs/code-structure.md rewritten for Go | **FIXED** |
| H-08 | WorkflowWaiting.ResetTime populated | **FIXED** |

### Medium

| ID | Title | Status |
|----|-------|--------|
| M-01 | implementation-assumptions.md state dir path | **FIXED** |
| M-02 | debug-mode.md directory format | **FIXED** |
| M-03 | console-output.md stale note | **FIXED** |
| M-04 | Step-number logic consolidated | **NOT FIXED** |
| M-05 | StateTypeMarkdown / StateTypeScript constants | **FIXED** |
| M-06 | os.Remove errors logged | **FIXED** |
| M-07 | FileExists error exposure | **FIXED** |
| M-08 | AgentPaused.Reason typed constants | **NOT FIXED** |
| M-09 | Path-traversal check in zipscope | **FIXED** |
| M-10 | nil-means-no-change contract documented | **NOT FIXED** |
| M-11 | plan.md updated to sequential design | **FIXED** |
| M-12 | Budget-after-cost behavior commented | **FIXED** |
| M-13 | Verbose doc updated | **FIXED** |
| M-14 | docs/code-structure.md (same as H-07) | **FIXED** |
| M-15 | orchestrator test for normal state persistence | **FIXED** |
| M-16 | Policy mixed-case TargetsMatch tests | **FIXED** |

### Low & Info

| ID | Title | Status |
|----|-------|--------|
| L-01 | Reporter any field removed | **FIXED** |
| L-02 | Width int removed or wired | **FIXED** |
| L-03 | README.md no pip install | **FIXED** |
| L-04 | GetStateDir result cached | **FIXED** |
| L-05 | ExtractCostFromResults edge-case tests | **FIXED** |
| L-06 | Empty-stack allocation skipped | **FIXED** |
| L-07 | orchestration-design.md asyncio references | **FIXED** |
| L-08 | implementation-assumptions.md src/config.py references | **FIXED** |
| L-09 | Windows process management | **PARTIALLY FIXED** |
| L-10 | bus_test.go handler-order comment | **FIXED** |
| L-11 | parsing_test.go empty attribute value test | **FIXED** |
| I-01 | Python tone in docs | **FIXED** |
| I-02 | fromState used or removed | **FIXED** |
| I-03 | docs/sample-workflows.md build note | **FIXED** |

---

## Open Findings

### FN-001 · AgentPaused.Reason raw strings — no typed constants

**Status of prior finding**: NOT FIXED (was M-08)
**Severity**: Medium
**Files**: `internal/events/events.go:104-108`, `internal/orchestrator/orchestrator.go:369,463-473`

**Description**: The `AgentPaused` struct's `Reason string` field is populated with raw string literals throughout the codebase:
- `"usage limit"` (orchestrator.go:369)
- `"timeout"` (orchestrator.go:466)
- `"prompt_error"` (orchestrator.go:470)
- `"claude_error"` (orchestrator.go:473)

No constants are defined in the `events` package for these values, making them fragile (typo-prone) and hard to match against in observers or tests.

**Fix**: Define constants in `internal/events/events.go`:
```go
const (
    PauseReasonUsageLimit  = "usage limit"
    PauseReasonTimeout     = "timeout"
    PauseReasonPromptError = "prompt_error"
    PauseReasonClaudeError = "claude_error"
)
```
Then replace all raw string literals in `orchestrator.go` with these constants.

---

### FN-002 · nil-means-no-change contract undocumented in WriteState

**Status of prior finding**: NOT FIXED (was M-10)
**Severity**: Medium
**Files**: `internal/state/state.go:136`

**Description**: The `WriteState` function accepts fields that may be nil to indicate "no change", but this contract is not documented in the function signature or a comment. Future maintainers may not know that passing nil preserves the existing value, or may accidentally pass nil when they intended to clear a field.

**Fix**: Add a doc comment to `WriteState` (and to the interface if one exists) explaining the nil-means-no-change semantics for pointer/slice fields.

---

### FN-003 · Step-number/state-name abbreviation logic duplicated

**Status of prior finding**: NOT FIXED (was M-04)
**Severity**: Medium
**Files**: `internal/transitions/transitions.go:250-257`, `internal/executors/executors.go:86-97`

**Description**: State-name extraction and abbreviation logic (truncating to 6 chars) is duplicated in `transitions.go` inline rather than being consolidated into a shared function. Both `transitions.go` and `executors.go` call `parsing.ExtractStateName`, but the abbreviation (first-6-chars truncation) lives only in `transitions.go` as inline code. If the abbreviation rule changes, it must be updated in multiple places.

**Fix**: Extract the abbreviation logic into a named function in `internal/parsing` or a shared utility, and call it from both `transitions.go` and any other place that needs the same behavior.

---

### FN-004 · Debug observer silently ignores all I/O errors

**Status of prior finding**: N/A (new)
**Severity**: Medium
**Files**: `internal/observers/debug/debug.go:78-82,116-120`

**Description**: Both `json.Marshal` errors and `os.OpenFile` errors in the debug observer are silently dropped with bare `return` statements. When debug output silently fails, the developer has no indication that their debug data is not being captured — which defeats the purpose of debug mode. The errors should at minimum be written to stderr.

```go
// Current code (silent discard):
if err != nil {
    return
}
```

**Fix**: Log I/O errors in the debug observer to stderr so developers know when debug output is failing:
```go
if err != nil {
    fmt.Fprintf(os.Stderr, "debug observer: %v\n", err)
    return
}
```

---

### FN-005 · Windows process tree termination incomplete (documented limitation)

**Status of prior finding**: PARTIALLY FIXED (was L-09)
**Severity**: Low
**Files**: `internal/platform/platform_windows.go:41-45`

**Description**: The Windows implementation acknowledges (in a comment) that it only terminates the root process, not child processes spawned by scripts. Child processes may become orphans. A Windows Job Object would solve this, but is not implemented. The comment documents the limitation clearly.

**No fix required** — limitation is documented and acceptable. Noted here for completeness.

---

### FN-006 · console-output.md design status ambiguous

**Status of prior finding**: N/A (new)
**Severity**: Info
**Files**: `docs/console-output.md`

**Description**: The document contains both "Current Output Format" and "Proposed Output Format" sections but does not clearly mark which format is actually implemented. A reader cannot tell whether the document is a historical record, an in-progress design, or a complete specification. This is minor documentation clarity.

**Fix**: Add a brief note at the top of the document indicating its status (e.g., "This document describes the implemented output format as of …").

---

## New-Finding-Only Summary

| ID | Title | Severity | Status |
|----|-------|----------|--------|
| FN-001 | AgentPaused.Reason raw strings | Medium | **FIXED** |
| FN-002 | nil-means-no-change undocumented | Medium | **FIXED** |
| FN-003 | State-name abbreviation duplicated | Medium | Closed (false alarm — logic exists in one place only) |
| FN-004 | Debug observer silently ignores errors | Medium | **FIXED** |
| FN-005 | Windows process tree incomplete | Low | Open (acceptable, limitation documented) |
| FN-006 | console-output.md status ambiguous | Info | Open |
