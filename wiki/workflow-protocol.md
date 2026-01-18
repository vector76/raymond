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

The orchestrator's error handling behavior depends on whether the state has
defined `allowed_transitions` in its YAML frontmatter:

**If `allowed_transitions` are defined:**
- If the output contains **no valid tag**, **multiple tags**, or a **policy
  violation**, the orchestrator generates a reminder prompt listing all
  allowed transitions and re-prompts the agent.
- The reminder prompt is automatically generated from the `allowed_transitions`
  YAML definition, showing the agent exactly which transitions are permitted.
- After a small number of reminder attempts (default: 3), persistent failures
  mark the workflow as failed.

**If `allowed_transitions` are NOT defined (no frontmatter or empty list):**
- Parse failures (no tag, multiple tags) are treated as errors and the agent
  terminates immediately.
- Without the YAML definition, the orchestrator cannot generate a meaningful
  reminder prompt, so it cannot recover from parse failures.

## Per-State Policy (Allowed Transitions)

Workflows often want to restrict what a given state is allowed to do (e.g. "this
state may not fork"). The orchestrator should treat each prompt file as having
an optional **policy** declared in YAML frontmatter.

The policy lists allowed transition combinations explicitly. Each entry specifies
a tag and its required attributes (target, return, next, etc.).

Example with simple transitions:

```yaml
---
allowed_transitions:
  - { tag: goto, target: REVIEW.md }
  - { tag: goto, target: DONE.md }
  - { tag: result }
---
```

Multi-line format (equivalent):

```yaml
---
allowed_transitions:
  - tag: goto
    target: REVIEW.md
  - tag: goto
    target: DONE.md
  - tag: result
---
```

Structured transitions with attributes:

```yaml
---
allowed_transitions:
  - { tag: goto, target: NEXT.md }
  - tag: call
    target: RESEARCH.md
    return: SUMMARIZE.md
  - tag: function
    target: EVAL.md
    return: NEXT.md
  - tag: fork
    target: WORKER.md
    next: CONTINUE.md
  - { tag: result }
---
```

Validation rules:

- The agent must emit a transition that exactly matches one of the entries in
  `allowed_transitions` (tag, target, and all attributes must match).
- If no matching entry is found, treat as an error and re-prompt (same as
  error and re-prompt (same as “zero/multiple tags”).
- If `allowed_transitions` is not specified (no frontmatter or empty list), all
  transitions are allowed, but parse failures (no tag, multiple tags) will
  terminate the agent immediately since no reminder can be generated.

**Reminder Prompt Generation:**

When `allowed_transitions` are defined, the orchestrator automatically generates
reminder prompts from the YAML definition. The reminder lists all permitted
transitions in a clear format, helping the agent understand exactly which tags
and targets are valid for the current state. This makes it possible to recover
from parse failures and policy violations by re-prompting with helpful guidance.

**Important:** Without YAML frontmatter defining `allowed_transitions`, parse
failures cannot be recovered because the orchestrator has no way to know which
transitions are expected and cannot generate a meaningful reminder prompt.

## Model Selection

The YAML frontmatter can also specify which Claude model to use for a state.

**Frontmatter field:**

```yaml
---
model: sonnet
allowed_transitions:
  - { tag: goto, target: NEXT.md }
---
```

Valid values: `opus`, `sonnet`, `haiku`

**CLI parameter:**

The `--model` flag on `start` and `run` commands provides a default model for
all states that don't specify one in frontmatter.

```bash
raymond workflows/example/START.md --model sonnet
```

**Precedence:**

1. Frontmatter `model` field (highest priority) — overrides CLI default
2. CLI `--model` parameter — used if no frontmatter model specified
3. None — Claude Code uses its default when no `--model` flag is passed

This allows workflows to specify expensive models (opus) for complex reasoning
states while using cheaper models (haiku) for simple evaluations, with a
sensible default for everything else.

## Implicit Transitions (Optimization)

When a state's policy specifies **exactly one allowed transition** (and it is not
a `<result>` tag), the orchestrator may optimize by **assuming** that transition
instead of requiring the model to emit it explicitly.

**Conditions for implicit transition:**
- The policy specifies exactly one entry in `allowed_transitions`
- The transition is not `<result>` (result tags always require explicit emission
  because they carry variable payload content)
- All required information (tag, target, and all attributes) is predetermined
  in the policy

**Behavior:**
- If the model emits **no tag**, the orchestrator uses the implicit transition
  from the policy.
- If the model emits a tag, it **must match** the policy exactly. If it doesn't
  match, that is an error (same as a policy violation).
- This optimization saves tokens by not requiring the model to emit redundant
  transition tags when only one path is possible.

**Example:**

```yaml
---
allowed_transitions:
  - { tag: goto, target: NEXT.md }
---
```

In this case, the model doesn't need to emit `<goto>NEXT.md</goto>`. The
orchestrator will automatically use this transition if no tag is found. However,
if the model does emit `<goto>NEXT.md</goto>`, it will be validated and accepted.
If the model emits any other tag or a mismatched tag, it's an error.

**Result tags are always explicit:**
Even if a policy allows only `<result>`, the model must still emit the tag
because the payload content is variable and cannot be predetermined.
