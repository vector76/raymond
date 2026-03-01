# Cross-Workflow Calls and Forks

This document captures the design for cross-workflow invocation in Raymond: the
ability for a running workflow to invoke other workflows natively, either
blocking (like a subroutine call) or non-blocking (like spawning independent
workers). It covers motivation, design decisions, tag semantics, implementation
requirements, and constraints — including the nuances that informed each
decision.

## Motivation

Raymond workflows are scoped to a single directory (or zip archive). This
design keeps them self-contained and prevents accidental cross-scope
transitions. However, it creates friction when multiple distinct workflows need
to be composed:

- **Sequential composition**: Run `feat-to-beads` (turns a feature document
  into a structured task list), then run `bs_work` (implements those tasks).
  Currently this requires a wrapper bash script that invokes raymond twice.
- **Parallel workers**: Run multiple instances of `bs_work` in parallel across
  separate git worktrees. Currently requires manually launching separate raymond
  processes in separate shells.

A bash wrapper works but is awkward: there is no shared budget, no unified
crash recovery, and the composition logic lives outside raymond. The goal is
native cross-workflow support that keeps all of this inside a single raymond
instance.

## Design Principle: Single Raymond Instance

The critical design decision is that sub-workflows run **within the same
raymond orchestrator instance** — not as child processes. This means:

- **Shared budget**: All workflows debit the same `total_cost_usd` counter.
  No inter-process coordination needed.
- **Shared state file**: All agent state lives in the same JSON state file.
  `<fork-workflow>` adds new agent entries (like `<fork>`); `<call-workflow>`
  and `<function-workflow>` transition the existing agent into the sub-workflow
  scope (like `<call>` and `<function>`) without creating new agent entries.
  Crash recovery via `raymond --resume` restores the entire composition
  automatically.
- **Shared event bus**: All events (cost, state transitions, agent lifecycle)
  are visible to a single console observer.
- **No IPC**: No pipes, no stdout parsing between processes, no exit code
  interpretation.

The trade-off is that the orchestrator must support per-agent scope directories
(see [Implementation Requirements](#implementation-requirements) below).

## Three New Tags

Cross-workflow support is implemented as three new transition tags, designed as
analogues to the existing `<call>`, `<function>`, and `<fork>` tags. The
`-workflow` suffix marks the cross-scope boundary explicitly.

| Tag | Blocks parent? | `cd` override | Session | Analogue |
|-----|---------------|--------------|---------|---------|
| `<call-workflow>` | yes | no | fork caller's | `<call>` |
| `<function-workflow>` | yes | yes | fresh | `<function>` |
| `<fork-workflow>` | no | yes | fresh | `<fork>` |

### Why distinct tag names?

The existing tags (`<call>`, `<fork>`, etc.) take a **state filename** as
content — a bare name scoped to the current workflow directory. Cross-workflow
tags take a **workflow specifier** as content — a path to another folder, file,
or zip archive. These are categorically different operands, and mixing the two
in the same tag would be an abstraction error.

Distinct tag names also provide a safety boundary enforced by the policy system
(see [Policy and Safety](#policy-and-safety)): a state that only allows `<call>`
cannot accidentally invoke a cross-workflow call, because `<call-workflow>` is
a different tag.

## Workflow Specifier Syntax

The content of all three cross-workflow tags is a **workflow specifier** — the
same grammar accepted by `raymond --start` on the command line:

| Specifier form | Meaning |
|----------------|---------|
| `../other-workflow/` | Directory; entry point resolved to `1_START.md` |
| `../other-workflow/1_START.md` | Explicit entry point file |
| `../other-workflow.zip` | Zip archive; entry point is `1_START.md` inside |

The scope directory for the invoked workflow is derived from the specifier:
- For a directory or `.md` file specifier, scope = the containing directory.
- For a zip specifier, scope = the zip archive (existing zip scope logic).

### Path normalization

Workflow specifier paths are **normalized to the OS path separator** when
parsed. This allows workflow files authored on one platform to run on another:

- `../feat-to-beads/1_START.md` works on Windows (normalized to `..\\feat-to-beads\\1_START.md`)
- `..\feat-to-beads\1_START.md` works on Linux (normalized to `../feat-to-beads/1_START.md`)

### Scope-relative resolution

Relative paths in workflow specifiers are resolved relative to the **calling
agent's scope directory** — not the raymond launch directory and not the
agent's `cwd` (working directory). The `scope_dir` and `cwd` are distinct:

- `scope_dir`: where the workflow's state files live (determines how
  cross-workflow specifiers are resolved)
- `cwd`: where Claude Code and shell scripts execute (a runtime environment
  detail, independent of the workflow definition)

This separation makes workflow repositories self-contained. A collection of
workflows stored in `~/.workflows/` can invoke each other with `../sibling/`
regardless of where raymond is launched from or what project directory Claude
is operating in.

**Resolution algorithm:**

For a **directory scope** at `/wf/workflow2/`:
```
../workflow1.zip    →  /wf/workflow1.zip
../workflow3/       →  /wf/workflow3/
```

For a **zip scope** at `/wf/workflow1.zip`:

The zip file is treated as a virtual folder at its own filesystem location.
The base for resolution is the zip file's path treated as a directory — i.e.,
`../` from inside the zip navigates to `/wf/` (the directory containing the
zip). The virtual base is derived from the **zip filename**, not the internal
folder name if the zip uses a single-folder layout. Example:

```
# zip at /wf/workflow1.zip  (internal layout irrelevant)
../workflow2/       →  /wf/workflow2/
../workflow3.zip    →  /wf/workflow3.zip
```

If the zip internally contains `mywf/1_START.md`, the internal folder name
`mywf` is a packaging artifact and is ignored for resolution purposes. The
virtual scope is always `workflow1/` (zip filename minus `.zip`), not `mywf/`.

**Absolute paths** bypass resolution entirely and are used as-is. Absolute
specifiers are valid but reduce portability; relative specifiers are preferred
in workflow repositories.

**At launch time**, when raymond is started with a relative path (e.g.,
`raymond ./my-workflows/feat/`), the scope directory is resolved to an
**absolute path** immediately and stored as such in the state file. This
ensures that resumed workflows and sub-workflow specifiers resolve correctly
regardless of the current directory at resume time.

### Entry point resolution for directory specifiers

When the specifier is a directory (no `.md` extension, no `.zip` extension), the
entry point is `1_START.md` inside that directory. This mirrors the convention
already used for `raymond workflow/` on the command line. If `1_START.md` does
not exist, the invocation is an error at dispatch time.

## Tag Semantics

### `<call-workflow return="NEXT.md">../other-wf/</call-workflow>`

Blocking invocation of a sub-workflow. The current agent transitions its scope
into the sub-workflow — it runs the sub-workflow's states directly, within the
same agent thread. When `<result>` is emitted from within the sub-workflow, the
agent's scope is restored to the caller via the stack frame, and the result
payload is injected as `{{result}}` into `NEXT.md`. No new agent entry is
created.

**Attributes:**
- `return` (required): State in the **caller's** scope to resume after the
  sub-workflow completes.
- `input` (optional): String injected as `{{result}}` into the sub-workflow's
  entry state, equivalent to `raymond --input "..."`.
- `cd` attribute: **Not permitted — a dispatch-time error.** See
  [Session Constraints on `call-workflow`](#session-constraints-on-call-workflow).

**Stack behavior:** Pushes a frame containing the caller's session ID, caller's
scope directory, caller's `cwd`, and the return state. When `<result>` is
emitted within the sub-workflow, the frame is popped and all caller context is
restored.

**Session behavior:** The sub-workflow's first state runs via `--fork-session`
from the caller's session, giving it access to the caller's conversation context
(same semantics as `<call>`).

### `<function-workflow return="NEXT.md">../other-wf/</function-workflow>`

Blocking invocation of a sub-workflow, with a fresh session (no context
inheritance). The current agent transitions its scope into the sub-workflow,
running its states directly within the same agent thread until `<result>` is
emitted. No new agent entry is created.

**Attributes:**
- `return` (required): State in the caller's scope to resume after completion.
- `input` (optional): String injected as `{{result}}` into the sub-workflow's
  entry state.
- `cd` (optional): Working directory for the sub-workflow's agents. If
  omitted, inherits the caller's `cwd`.

**Stack behavior:** Same as `<call-workflow>` — pushes a frame, pops on result.

**Session behavior:** Sub-workflow starts in a fresh session (no
`--fork-session`). Same semantics as `<function>`.

**Use case:** Invoking a sub-workflow in a different git worktree or checkout.
Because sessions are fresh, the `cwd` can be freely changed without breaking
`--resume` behavior.

### `<fork-workflow next="NEXT.md">../other-wf/</fork-workflow>`

Non-blocking spawn of an independent sub-workflow agent. The calling agent
immediately continues at `next`. The spawned workflow agent runs independently
and does not return a result to the caller. The spawned agent's terminal
`<result>` payload, if any, is discarded — there is no mechanism for the
calling agent to observe or receive it.

**Attributes:**
- `next` (optional when `<goto>` is present in the same output; see
  [Multi-tag Outputs](#multi-tag-outputs)).
- `input` (optional): String injected as `{{result}}` into the spawned
  workflow's entry state.
- `cd` (optional): Working directory for the spawned workflow. If omitted,
  inherits the caller's `cwd`.

**Stack behavior:** The spawned agent starts with an empty stack. The calling
agent's stack is preserved (identical to `<fork>`).

**Session behavior:** Spawned agent starts with a fresh session (no context
inheritance). Same semantics as `<fork>`.

**Use case:** Launching parallel workers — e.g., three `bs_work` instances
processing tasks from a shared queue in separate worktrees, while the main
agent proceeds to a monitoring loop.

## Session Constraints on `call-workflow`

`<call-workflow>` does not permit a `cd` attribute. This constraint is
fundamental, not arbitrary.

Claude Code session IDs are bound to a working directory. When raymond invokes
Claude Code with `--resume <session-id>` or `--fork-session <session-id>`,
Claude Code locates the session in the session store rooted at the current
working directory (typically `.claude/` within the CWD). If the working
directory changes, the session store changes, and the session ID is no longer
resolvable — `--fork-session` silently fails or errors.

`<call-workflow>` uses `--fork-session` to give the sub-workflow access to the
caller's conversation context. Allowing a different `cwd` would break this
invariant silently.

`<function-workflow>` and `<fork-workflow>` start with fresh sessions
(`session_id = nil`), so they are immune to this constraint. The `cd`
attribute is permitted on both.

**Consequence for workflow authors:** If you need a blocking call to a
sub-workflow in a different directory (e.g., a different worktree), use
`<function-workflow>`, not `<call-workflow>`. The sub-workflow will not have
access to the caller's conversation history, but it can operate freely in its
own directory.

## The `input` Attribute

The `input` attribute on all three tags injects a string as `{{result}}` into
the sub-workflow's entry state. This is semantically identical to running:

```bash
raymond ../other-wf/ --input "some data"
```

The input value is stored as `pending_result` on the spawned agent before its
first state executes, then cleared after the first state runs — exactly as
`--input` and the normal `<result>` return mechanism work.

**Use cases:**
- Pass a file path the sub-workflow should process.
- Pass a task ID or configuration string.
- Pass data from the calling agent's prior result: an LLM state that received
  `{{result}}` in its prompt can include that content directly as the `input`
  attribute value. A script state can similarly construct the tag dynamically
  with shell variable expansion.

## Multi-tag Outputs

### Multiple `<fork>` and `<fork-workflow>` Tags

A single state output may contain **multiple `<fork>` tags, multiple
`<fork-workflow>` tags, or a mix of both**. Each tag spawns one independent
agent. This enables a shell script to read a config file and spawn one worker
per entry:

```bash
#!/bin/bash
while IFS= read -r worktree; do
  echo "<fork-workflow cd=\"$worktree\" input=\"$worktree\">../bs_work/</fork-workflow>"
done < worktrees.txt
echo "<goto>MONITOR.sh</goto>"
```

### Parent Continuation: `next` vs `<goto>`

The parent agent needs exactly one continuation target after all forks are
spawned. There are two equivalent ways to specify it:

**Via `next` attribute on fork tags:**
```xml
<fork-workflow next="MONITOR.sh" cd="/wt/A">../bs_work/</fork-workflow>
<fork-workflow next="MONITOR.sh" cd="/wt/B">../bs_work/</fork-workflow>
```

**Via a `<goto>` tag:**
```xml
<fork-workflow cd="/wt/A">../bs_work/</fork-workflow>
<fork-workflow cd="/wt/B">../bs_work/</fork-workflow>
<goto>MONITOR.sh</goto>
```

**Both mechanisms together (allowed if they agree):**
```xml
<fork-workflow next="MONITOR.sh" cd="/wt/A">../bs_work/</fork-workflow>
<goto>MONITOR.sh</goto>
```
This is redundant but permitted — an LLM or script author who specifies both
will succeed as long as the targets match.

### Rules for multi-tag fork outputs

1. **At least one continuation target required.** At least one fork tag must
   specify `next`, OR a `<goto>` tag must be present.
2. **All `next` values must agree.** If multiple fork tags specify `next`, they
   must all name the same target. Mismatched `next` values are an error.
3. **If `<goto>` and `next` are both present, they must agree.** The targets
   must match. Mismatch is an error.
4. **Exactly one `<goto>` permitted.** Even if a `<goto>` would match a
   redundant second `<goto>`, only one is allowed.
5. **Only `<goto>` may accompany fork tags.** Combining fork tags with
   `<call>`, `<function>`, `<reset>`, `<result>`, `<call-workflow>`, or
   `<function-workflow>` in the same output is not permitted. The semantics
   would be ambiguous or contradictory.
6. **`<fork>` and `<fork-workflow>` may be freely mixed** in the same output,
   subject to the rules above.

### Why `next` was originally required

The `next` attribute exists for historical reasons: when multiple transition
tags per output were not supported, `<fork>` needed an inline way to specify
both the worker's target and the parent's continuation in a single tag. Now
that multiple tags are allowed, `<goto>` is an equally valid way to express
the parent's continuation. Both are supported for compatibility and flexibility.

## Non-Rejoining Workers (No Join Primitive Needed)

A common pattern is spawning N workers that process items from a shared queue,
with a coordinator that monitors progress:

```
Main agent:
  SPAWN_WORKERS.sh  →  spawns 3 fork-workflow workers  →  MONITOR.sh
                                                           (polls until all work done)
Worker agents:
  bs_work/1_START.md  →  ... claim/implement/commit loop  →  <result> (terminates)
```

The main agent does not wait for the workers. The workers terminate
independently when they run out of work. `MONITOR.sh` polls an external source
(e.g., a task database) to detect completion. No special join primitive is
needed in raymond — the coordination is handled by the work queue itself.

This avoids the complexity of cross-agent synchronization primitives, which
would require significant orchestrator machinery and introduce race conditions.

## Working Directories for Parallel Workers

For the parallel worktree case (each `bs_work` instance in a separate git
worktree), the `cd` attribute sets the worker's working directory:

```xml
<fork-workflow next="MONITOR.sh" cd="/repo/worktrees/feature-a" input="feature-a">../bs_work/</fork-workflow>
<fork-workflow next="MONITOR.sh" cd="/repo/worktrees/feature-b" input="feature-b">../bs_work/</fork-workflow>
```

**Dynamic `cd` values (run-time determined paths):** The `cd` value in a tag
attribute is a static string at parse time. For cases where the CWD is
determined at runtime (e.g., from a config file listing available worktrees), a
shell script state should compute the fork tags dynamically:

```bash
#!/bin/bash
while IFS= read -r entry; do
  worktree_path=$(echo "$entry" | jq -r '.path')
  task_input=$(echo "$entry" | jq -r '.input')
  echo "<fork-workflow next=\"MONITOR.sh\" cd=\"$worktree_path\" input=\"$task_input\">../bs_work/</fork-workflow>"
done < worktree_config.json
```

For cases where the sub-workflow itself needs to determine its working directory
(e.g., from an environment variable or config file internal to the workflow),
the sub-workflow's entry state can use `<reset cd="...">` to change its own
`cwd` after reading the configuration. This keeps the worktree configuration
within the sub-workflow rather than requiring the caller to know it.

## Policy and Safety

The `allowed_transitions` frontmatter mechanism already enforces which
transition tags a state may emit. The new tags participate in this system:

```yaml
---
allowed_transitions:
  - { tag: goto, target: NEXT.md }
  - tag: call-workflow
    return: IMPL.md
  - tag: fork-workflow
    next: MONITOR.sh
---
```

Because `<call-workflow>` and `<fork-workflow>` are different tag names from
`<call>` and `<fork>`, a state that only allows `<call>` **cannot** emit
`<call-workflow>` — the policy prevents it. Workflow authors must explicitly
opt in to cross-workflow invocation.

For the multi-fork case, the policy entry for `<fork-workflow>` (and `<fork>`)
does not need to exhaustively list every possible combination. Policy validation
for multi-fork outputs checks that each fork tag's type is in the allowed list;
the `next` consistency rules are checked separately as structural validation.

## Nesting Depth Limit

Sub-workflows can themselves invoke sub-workflows, creating a call tree. To
prevent runaway nesting (and cycles), the orchestrator enforces a hard maximum
nesting depth of **4 levels** for `<call-workflow>` and `<function-workflow>`.
`<fork-workflow>` workers count toward this limit based on the depth of the
agent that spawned them.

At depth 4, any attempt to emit a `<call-workflow>` or `<function-workflow>`
tag is treated as a policy violation and the agent is paused with an
explanatory error.

Cycle detection (workflow A calls workflow B which calls workflow A) is
implicitly handled by the nesting limit in practice, though implementors may
add explicit cycle detection as an improvement.

## Implementation Notes

### Per-agent scope directory

`scope_dir` is stored per-agent on `AgentState`, not at the workflow level.
This allows different agents to execute in different workflow scopes
simultaneously. The initial (main) agent is assigned `scope_dir` from the
launch parameters; forked and called agents inherit or override it per their
tag. State files written before this field existed are migrated on load by
copying the workflow-level `scope_dir` to each agent.

### Stack frames

Stack frames (pushed by `<call>`, `<function>`, `<call-workflow>`,
`<function-workflow>`) save and restore `scope_dir`, `cwd`, and the
cross-workflow nesting depth:

```go
type StackFrame struct {
    Session      *string  // caller's Claude Code session ID
    State        string   // return state (filename in caller's scope)
    ScopeDir     string   // caller's scope directory (restored on result)
    Cwd          string   // caller's working directory (restored on result)
    NestingDepth int      // caller's nesting depth (restored on result)
}
```

When `<result>` pops a frame, `ScopeDir`, `Cwd`, and `NestingDepth` are all
restored, so the calling agent resumes in exactly the context it was in before
the sub-workflow was invoked.

### Workflow specifier parsing

The workflow specifier (tag content) is parsed at transition dispatch time:
1. Normalize path separators for the current OS.
2. Resolve the path relative to the calling agent's `scope_dir` (not `cwd`,
   not the raymond launch directory). For a zip scope, the virtual base is the
   zip file's path treated as a directory (zip filename minus `.zip`).
3. Store as an absolute path in the agent state.
4. Determine scope: directory → scope is that directory; `.md` file → scope is
   its containing directory; `.zip` file → scope is the zip (existing logic).
5. Resolve entry point: directory scope → `1_START.md`; `.md` specifier →
   the specified file; zip → `1_START.md` inside the archive.

### Session handling for `call-workflow`

`<call-workflow>` uses `--fork-session` from the caller's session, identical to
`<call>`. The `ForkSessionID` transient field on `AgentState` signals this to
the executor. Because `call-workflow` does not permit a `cd` attribute, the
session remains valid in the sub-workflow's execution environment.

### Agent naming for cross-workflow agents

Agents spawned by cross-workflow tags follow the existing naming convention
(parent ID + state abbreviation + counter), but the state abbreviation is
derived from the **workflow specifier** rather than a filename:

- `../bs_work/` → abbreviation `bs_wor` (directory base name, lowercased, capped at 6)
- `../feat-to-beads/` → abbreviation `feat-t`
- `../wf.zip` → abbreviation `wf` (stem only, no truncation needed)

This keeps agent IDs readable and traceable in logs.

## Example: Sequential Composition

Feature development pipeline: decompose a feature, then implement.

```bash
# Launch the top-level coordinator
raymond top-level/
```

```markdown
<!-- top-level/1_START.md -->
---
allowed_transitions:
  - tag: call-workflow
    return: IMPL_PHASE.md
---
The feature document is in feature.md.

Invoke the planning workflow to decompose it into tasks:
<call-workflow return="IMPL_PHASE.md" input="feature.md">../feat-to-beads/</call-workflow>
```

```markdown
<!-- top-level/IMPL_PHASE.md -->
---
allowed_transitions:
  - tag: call-workflow
    return: DONE.md
---
The beads have been created. Now invoke the implementation workflow:

<call-workflow return="DONE.md">../bs_work/</call-workflow>
```

```markdown
<!-- top-level/DONE.md -->
---
allowed_transitions:
  - { tag: result }
---
Implementation complete. Result: {{result}}

<result>pipeline complete</result>
```

## Example: Parallel Workers with Monitoring

Three `bs_work` instances in separate worktrees, main thread monitors:

```bash
#!/bin/bash
# SPAWN.sh - reads worktrees.txt and spawns one worker per line
while IFS= read -r wt_path; do
  echo "<fork-workflow cd=\"$wt_path\">../bs_work/</fork-workflow>"
done < worktrees.txt
echo "<goto>MONITOR.sh</goto>"
```

```bash
#!/bin/bash
# MONITOR.sh - polls task database; exits when all closed
pending=$(curl -s "$BEADS_API/tasks?status=open" | jq 'length')
if [ "$pending" -eq 0 ]; then
  echo "<result>all tasks complete</result>"
else
  sleep 30
  echo "<reset>MONITOR.sh</reset>"
fi
```

The three worker agents run the full `bs_work` loop (claim task → implement →
commit → push) and terminate independently. The main agent loops in
`MONITOR.sh` until the task queue is empty. No join primitive is needed.

## Relationship to Existing Tags

The new tags are additive. All existing tags (`<goto>`, `<reset>`, `<call>`,
`<function>`, `<fork>`, `<result>`) are unchanged. The new tags layer on top:

| Existing tag | Cross-workflow analogue | Key difference |
|---|---|---|
| `<call>` | `<call-workflow>` | Content is workflow spec, not state filename; no `cd` |
| `<function>` | `<function-workflow>` | Content is workflow spec; `cd` permitted |
| `<fork>` | `<fork-workflow>` | Content is workflow spec; `cd` permitted |

There is no `<goto-workflow>` or `<reset-workflow>`: these transitions are
intra-agent by nature. Crossing a workflow boundary always involves either
suspending and returning (call/function) or spawning an independent agent
(fork).
