# Reset: Stack Retention

This document describes a deliberate change to the semantics of `<reset>`:
the return stack is now **preserved** across a reset, not cleared. It explains
the motivation, the corrected semantics, the implementation change, and which
other documents need updating.

## Problem with the Original Semantics

The original `<reset>` specification cleared the return stack in addition to
starting a fresh Claude session. This was documented as the intended behavior,
with a warning that resetting with a non-empty stack was "usually a logic error."

In practice, this makes `<reset>` unusable inside any sub-workflow invoked via
`<call>` or `<function>`. Consider a sub-workflow that performs an iterative
loop — for example, repeatedly attempting a task until it succeeds:

```
CALLER --call(return=AFTER.md)--> LOOP.md
LOOP.md: attempt task
  → if not done: loop back to LOOP.md
  → if done: <result>done</result>
```

Using `<goto>` to loop accumulates conversation history on every iteration.
After many iterations the context window fills up, degrading quality or causing
errors. The natural remedy is `<reset>` — start a fresh session for each
iteration while continuing the loop. But under the original semantics, `<reset>`
inside this sub-workflow would discard the return frame, making it impossible
for the sub-workflow to ever return to its caller with `<result>`.

The two options available before this change were:
- **Use `<goto>`**: loops work but context grows unboundedly
- **Use `<reset>`**: context stays clean but the caller is permanently abandoned

Neither is acceptable for long-running iterative sub-workflows. The stack-
clearing behavior was never justified by a concrete use case — it was an
incidental implementation choice that happened to be documented as intentional.

## Corrected Semantics

`<reset>` now means: **start a fresh Claude session and continue at the target
state, preserving all other agent state including the return stack and `cwd`**.

The change is a single deletion from the transition handler: the line that
cleared the return stack is removed. Everything else about `<reset>` is
unchanged.

### Before

| Aspect | Behavior |
|--------|---------|
| Session | Cleared (fresh context) |
| Return stack | **Cleared** |
| Current state | Updated to target |
| CWD | Updated if `cd` attribute present; otherwise unchanged |

### After

| Aspect | Behavior |
|--------|---------|
| Session | Cleared (fresh context) |
| Return stack | **Preserved** |
| Current state | Updated to target |
| CWD | Updated if `cd` attribute present; otherwise unchanged |

## Updated Mental Model

`<reset>` is best understood as **`<goto>` with a fresh session**. It moves to
a new state (or the same state for looping) with a clean context window, but
the logical execution thread — including where to return to — is unchanged.

The session (`session_id`) is the only thing reset. The stack and working
directory are unaffected unless explicitly changed via the `cd` attribute.

## Use Cases Enabled

### Iterative loop within a subroutine

A sub-workflow can now loop with `<reset>` to bound context growth while still
returning to its caller:

```
CALLER --call(return=AFTER.md)--> LOOP.md
LOOP.md: do one iteration
  → if not done: <reset>LOOP.md</reset>    (fresh context, stack intact)
  → if done: <result>outcome</result>       (pops frame, returns to AFTER.md)
```

Each iteration of LOOP.md starts with a clean context window. The return frame
is preserved across every reset, so `<result>` still works correctly no matter
how many resets occurred.

### Long pipeline phases within a called sub-workflow

A sub-workflow invoked via `<call>` may itself have phase boundaries where
prior context is no longer useful. It can use `<reset>` to cross those
boundaries cleanly without sacrificing the ability to return:

```
CALLER --call(return=DONE.md)--> PHASE1.md
PHASE1.md writes output to file, emits <reset>PHASE2.md</reset>
PHASE2.md reads file, does work, emits <reset>PHASE3.md</reset>
PHASE3.md does final step, emits <result>summary</result>   → returns to DONE.md
```

## Abandoning the Entire Call Chain

The original "clear everything" behavior — abandoning the return stack
intentionally — has no documented use case. There is no dedicated "abort"
primitive. If a workflow genuinely needs to unwind early, each return state in
the call chain must propagate the abort by itself emitting `<result>`, so the
unwind happens one frame at a time as each return state executes. The cleaner
approach is to design workflows that don't need mid-chain aborts, or to use
`<fork-workflow>` for work that may need to be abandoned without affecting a
caller.

## Implementation Change

In `transitions/transitions.go`, the `HandleReset` function:

```go
// Before
agent.CurrentState = transition.Target
agent.SessionID = nil
agent.Stack = []StackFrame{}   // ← remove this line
if cd, ok := transition.Attributes["cd"]; ok {
    agent.Cwd = ResolveCd(cd, agent.Cwd)
}

// After
agent.CurrentState = transition.Target
agent.SessionID = nil
if cd, ok := transition.Attributes["cd"]; ok {
    agent.Cwd = ResolveCd(cd, agent.Cwd)
}
```

This is a one-line deletion. No other orchestrator logic requires changes.

## Documents Updated

The following documents were corrected to reflect the new semantics:

- **`docs/workflow-protocol.md`**: `Return stack: Cleared` → `Return stack: Preserved unchanged`; warning block about non-empty stack removed.
- **`docs/authoring-guide.md`**: `The return stack is also cleared` → `The return stack is preserved`.
- **`docs/orchestration-design.md`**: `<reset>` row changed from `Discarded (stack cleared)` → `Session discarded, stack preserved`.
- **`docs/console-output.md`**: Updated to say "reset clears the session but preserves the stack."
