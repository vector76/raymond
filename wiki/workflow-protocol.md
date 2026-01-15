# Workflow Protocol

This document is the **single source of truth** for Raymond's workflow language:
the required end-of-run tags, directory scoping, and the **return stack** model
for call/return semantics.

## Core Idea

Raymond interprets workflows as a sequence of Claude Code runs. Each run:

- Receives a prompt (a markdown file)
- Executes in a Claude Code session (fresh, resumed, or forked history)
- **Must terminate by emitting exactly one tag** from:
  - `<goto>...</goto>`
  - `<reset>...</reset>`
  - `<function ...>...</function>`
  - `<call ...>...</call>`
  - `<fork ...>...</fork>`
  - `<result>...</result>`

**Tag placement:** The single protocol tag may appear **anywhere** in the final
message from the agent.

**Protocol error:** If the output contains **zero** protocol tags or **multiple**
protocol tags, that is an error. The orchestrator should re-prompt with a short
reminder to emit exactly one valid tag.

## Workflow Scope (Directory Scoping)

A workflow is started from a specific prompt file path (e.g.
`workflows/coding/START.md` or `C:\path\to\START.md` during development).

- The directory containing the starting prompt is the workflow's **scope
  directory**.
- Any tag that references another prompt file (e.g. `<goto>REVIEW.md</goto>`)
  is resolved **only within the scope directory**.
- **Cross-directory transitions are not allowed.**

**Path safety rule:** Tag targets are treated as *filenames*, not paths. The
referenced filename must not contain `/` or `\` anywhere (no forward/backward
slashes), which prevents `../` and absolute/drive-root style references by
construction.

This allows having multiple workflow collections in separate subdirectories
without name collisions.

## The Return Stack

Raymond maintains a **return stack per live agent**. Each stack frame
represents "where to return to" when a callee produces `<result>...</result>`.

Conceptually, a frame contains:

- **resume session**: which Claude Code session to resume (the caller's session)
- **return state**: which prompt file to load next (within the same scope directory)

At return time, the `<result>...</result>` payload is injected into the return
state prompt (e.g. via `{{result}}`), but those values are not modeled as part
of the frame itself.

### Stack Operations

- **Push**: When the workflow executes a `<call ...>` or `<function ...>`, the
  orchestrator pushes a frame describing how to resume the caller afterward.
- **Pop**: When a Claude Code run ends with `<result>...</result>`, the
  orchestrator pops the stack:
  - If a frame exists: it resumes the frame's session and transitions to the
    frame's return state.
  - If the stack is empty: the agent terminates successfully.

Because workflows may contain multiple live agents (via `<fork>`), termination
with `<result>` and an empty return stack decreases the number of live agents by
one.

## Agent State Model (Multi-Agent)

A workflow may contain one or more live agents. The orchestrator persists agent
state in a structure like:

```json
{
  "agents": [
    {
      "id": "abc123",
      "stack": [
        {"session": "session_...", "state": "RETURN-STATE.md"},
        {"session": "session_...", "state": "RETURN-STATE-2.md"}
      ]
    }
  ]
}
```

- Each agent has a stable `id`.
- Each agent has its own return `stack` (possibly empty).
- Each frame has (at minimum) a `session` id to resume and a `state` (a prompt
  filename within the scope directory).

### Why this helps

The stack model makes return behavior robust even if the callee performs
multiple `goto` transitions before returning. Example:

```
S1 --call(return=S3)--> S2
S2 --goto--> S4
S4 --goto--> S5
S5 --result--> (pop) resume caller session and continue at S3
```

## Tag Semantics

### `<goto>FILE.md</goto>`

- **Meaning**: Continue within the same logical thread of control.
- **Return stack**: Preserved unchanged.
- **Session behavior**: Resume the current session (same context), then load
  `FILE.md` from the scope directory.

### `<reset>FILE.md</reset>`

- **Meaning**: Start a fresh session and continue at `FILE.md`.
- **Return stack**: Cleared (discard all pending returns).
- **Session behavior**: Start a new session with `FILE.md` (fresh context).

**Warning behavior:** A `<reset>` with a non-empty return stack is usually a
logic error (it discards the caller's pending return). The orchestrator should
log a warning when this occurs.

### `<call return="NEXT.md">CHILD.md</call>`

- **Meaning**: Enter a subroutine-like workflow that will eventually return a
  `<result>...</result>` to the caller.
- **Required attribute**: `return="NEXT.md"` (the state to continue at after return)
- **Return stack**: Push a frame for the caller (resume session + `NEXT.md`).
- **Session behavior**: Start the callee (typically by branching context from the
  caller so the callee can use relevant context without polluting the caller).

### `<function return="NEXT.md">EVAL.md</function>`

- **Meaning**: Run a stateless/pure evaluation task that returns to the caller.
- **Required attribute**: `return="NEXT.md"`
- **Return stack**: Push a frame for the caller (resume session + `NEXT.md`).
- **Session behavior**: Run in fresh context (no session history).

### `<fork next="NEXT.md" ...>WORKER.md</fork>`

- **Meaning**: Spawn an independent agent ("process-like") while the current
  agent continues.
- **Required attribute**: `next="NEXT.md"` (the state the parent continues at)
- **Return stack**:
  - **Worker**: starts with an empty return stack.
  - **Parent**: preserves its existing return stack (like `goto`).
- **Session behavior**:
  - **Worker** runs `WORKER.md` in a new workflow instance/session.
  - **Parent** continues by resuming its current session at `NEXT.md`.

`<fork>` increases the number of live agents by one.

### `<result>...</result>`

- **Meaning**: Return control to the most recent caller on the return stack, or
  terminate if no caller exists.
- **Return stack**: Pop one frame if present, otherwise terminate.
- **Payload**: The raw text between the tags is passed as-is (no summarization by
  the orchestrator). The return state's prompt receives it via `{{result}}`.

## Multi-tag Outputs

Multi-tag semantics (multiple transition tags in a single Claude Code output) are
**not supported initially**. The initial rule is:

- A run must end with **exactly one** of the six tags above.

## Error Handling (Protocol Level)

- If the output contains **no valid tag**, or **multiple tags**, the orchestrator
  should re-prompt with a short reminder to emit exactly one valid tag.
- Persistent protocol failures should mark the workflow as failed after a small
  number of retries.

## Per-State Policy (Allowed Tags / Allowed Targets)

Workflows often want to restrict what a given state is allowed to do (e.g. "this
state may not fork"). The orchestrator should treat each prompt file as having
an optional **policy** declared in YAML frontmatter.

Example:

```yaml
---
allowed_tags: [goto, result]
allowed_targets:
  goto: [REVIEW.md, DONE.md]
---
```

More detailed examples can constrain structured transitions:

```yaml
---
allowed_tags: [call, goto, result]
allowed_targets:
  goto: [NEXT.md]
  call:
    - child: RESEARCH.md
      return: SUMMARIZE.md
---
```

Validation rules:

- If the agent emits a protocol tag that is not in `allowed_tags`, treat as an
  error and re-prompt (same as “zero/multiple tags”).
- If the tag targets a filename not permitted by `allowed_targets`, treat as an
  error and re-prompt.

This policy is also a natural input into a future reminder prompt generator: the
orchestrator can remind the agent of the exact allowed tags/targets for the
current state.
