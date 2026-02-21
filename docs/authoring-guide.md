# Workflow Authoring Guide

This is the guide for writing Raymond workflow state files — the markdown
prompts and shell scripts that define your workflows. It covers everything you
need to know as a workflow author without getting into Raymond's internal
implementation.

For the authoritative protocol specification, see
[workflow-protocol.md](workflow-protocol.md). For architecture and internals,
see [orchestration-design.md](orchestration-design.md).

## Concepts

A **workflow** is a directory of state files that reference each other via
transition tags. Raymond runs these files in sequence, following the transitions
your prompts declare.

A **state file** is either:
- A **markdown prompt** (`.md`) — sent to Claude Code for LLM interpretation
- A **shell script** (`.sh` on Unix, `.bat` on Windows) — executed directly,
  no LLM involved

An **agent** is a logical thread of execution within a workflow. Workflows
start with one agent (`main`) and can spawn more via `<fork>`.

**Directory scoping:** All state files in a workflow must live in the same
directory. Transitions reference filenames only — no paths, no `/` or `\`
characters. The directory containing the starting file is the workflow's scope.

## Markdown States

Markdown states are prompts interpreted by Claude Code. Write them as you would
any Claude Code prompt, with one addition: instruct the agent to emit a
**transition tag** to signal what happens next.

```markdown
Review the code for issues. If everything looks good, emit:
<goto>COMMIT.md</goto>
```

The agent's final message must contain **exactly one** transition tag. The tag
can appear anywhere in the message.

### Scoping: Tell the Agent When to Stop

The Claude Code instance executing a state does not know it is part of a
workflow. It sees your prompt, nothing more. It has no awareness of other state
files, the overall workflow structure, or what steps come later. This means it
will try to be helpful by doing as much as it can — which often means racing
ahead and doing work that belongs to later steps.

To prevent this, every state prompt should clearly define its scope:

1. **State what to do.** Be specific about the task for *this* step.
2. **State when to stop.** Use explicit language like "STOP after writing the
   file" or "After reading the file, respond with exactly:
   `<goto>NEXT.md</goto>`".
3. **State what not to do yet.** If the agent can infer what logically comes
   next (and it will), tell it not to do that yet. Use the word **"yet"** or
   phrases like **"that happens in a later step"** — this prevents the
   constraint from lingering in context and blocking later steps that *should*
   perform those actions.

**Example — too vague:**

```markdown
Read the requirements file. Once done, proceed to the next step:
<goto>GENERATE.md</goto>
```

The word "proceed" is ambiguous. The agent may interpret it as permission to
keep going and start doing the work that `GENERATE.md` is supposed to handle.

**Example — explicit scope:**

```markdown
Read the requirements from requirements.md.

STOP after reading the file. Do not generate any code or create any files
yet — that happens in a later step.

After reading the file, respond with exactly:
<goto>GENERATE.md</goto>
```

For states with **implicit transitions** (single allowed transition in
frontmatter), the agent doesn't need to emit a tag, but scope boundaries are
still important. The agent can still race ahead and do work belonging to later
steps:

```markdown
---
allowed_transitions:
  - { tag: goto, target: REVIEW.md }
---
Create a plan in plan.md covering all the requirements.

STOP after writing plan.md. Do not start implementing yet — that happens
in a later step.
```

### YAML Frontmatter

Markdown states can optionally include YAML frontmatter to declare policy:

```yaml
---
allowed_transitions:
  - { tag: goto, target: REVIEW.md }
  - { tag: goto, target: DONE.md }
  - { tag: result }
---
Your prompt text here...
```

Frontmatter enables two features:

**1. Transition validation:** The orchestrator enforces that the agent's output
matches one of the declared transitions. If it doesn't, the agent gets
re-prompted with a reminder listing the valid options (up to 3 retries).

**2. Implicit transitions:** When frontmatter declares exactly one allowed
transition (and it's not `<result>`), the agent doesn't need to emit the tag at
all — the orchestrator assumes it. This saves tokens when only one path is
possible.

```yaml
---
allowed_transitions:
  - { tag: goto, target: NEXT.md }
---
Do the work. You don't need to emit a transition tag.
```

**Without frontmatter**, all transitions are allowed but the orchestrator
cannot recover from missing or invalid tags — failures are fatal.

### Model Selection

Frontmatter can specify which Claude model to use:

```yaml
---
model: haiku
allowed_transitions:
  - { tag: result }
---
Is this task complete? Reply with <result>YES</result> or <result>NO</result>
```

Valid values: `opus`, `sonnet`, `haiku`. This overrides the `--model` CLI flag.

Precedence: frontmatter `model` > CLI `--model` > Claude Code default.

Use this to run cheap evaluators on `haiku` while keeping complex reasoning
on `sonnet` or `opus`.

### Effort Level

Frontmatter can specify the effort level for extended thinking:

```yaml
---
effort: high
allowed_transitions:
  - { tag: goto, target: next.md }
---
Analyze this complex problem carefully...
```

Valid values: `low`, `medium`, `high`. This overrides the `--effort` CLI flag.

Precedence: frontmatter `effort` > CLI `--effort` > Claude Code default.

Use `high` effort for complex reasoning tasks, `medium` for balanced performance,
or `low` for simple tasks where speed is preferred over depth.

## Shell Script States

Shell scripts execute directly without invoking an LLM. Use them whenever
the work is deterministic and doesn't need reasoning: polling, builds, data
processing, environment setup, health checks, cleanup.

```bash
#!/bin/bash
# POLL.sh - Check for new issues

response=$(curl -s "$GITHUB_API/issues?state=open")
count=$(echo "$response" | jq 'length')

if [ "$count" -gt 0 ]; then
    echo "<goto>PROCESS.md</goto>"
else
    sleep 60
    echo "<reset>POLL.sh</reset>"
fi
```

Scripts emit the same transition tags to stdout. The orchestrator parses
them identically to LLM output.

**Key differences from markdown states:**

| Aspect | Markdown | Scripts |
|--------|----------|---------|
| Execution | LLM interprets prompt | Shell executes directly |
| Cost | Token cost per run | Zero |
| Error recovery | Can re-prompt on failures | Fatal on errors |
| Frontmatter policy | Supported | Not supported |

### Environment Variables

Scripts receive workflow context via environment variables:

| Variable | Description | Example |
|----------|-------------|---------|
| `RAYMOND_WORKFLOW_ID` | Workflow run identifier | `wf-2024-01-15-abc123` |
| `RAYMOND_AGENT_ID` | Current agent identifier | `main`, `main_worker1` |
| `RAYMOND_RESULT` | Result payload from a `<call>` return | (set only when returning from a call) |

### Persisting Data Between Script Runs

Scripts using `<reset>` to loop start fresh each time. To persist data across
iterations, write to files:

```bash
#!/bin/bash
counter_file="/tmp/poll_counter.txt"

if [ -f "$counter_file" ]; then
    count=$(cat "$counter_file")
else
    count=0
fi

count=$((count + 1))
echo $count > "$counter_file"

if [ $count -lt 5 ]; then
    echo "<reset>POLL.sh</reset>"
else
    rm -f "$counter_file"
    echo "<result>Polling complete after $count iterations</result>"
fi
```

### Error Handling

Script errors are **fatal** — no retries, no re-prompting. If a script exits
with a non-zero code, outputs no tag, or outputs multiple tags, the workflow
terminates. Ensure all code paths emit exactly one valid transition tag.

### Cross-Platform Scripts

Provide both `.sh` and `.bat` for the same state name to support both
platforms. Each platform uses its native version:

| Files present | Unix resolves to | Windows resolves to |
|---------------|-----------------|-------------------|
| `POLL.sh` | `POLL.sh` | Error |
| `POLL.bat` | Error | `POLL.bat` |
| `POLL.sh` + `POLL.bat` | `POLL.sh` | `POLL.bat` |

On Unix, `.sh` files run with `/bin/bash`. On Windows, `.bat` files run with
`cmd.exe`.

## Transition Tags

Every state must end by emitting exactly one of these tags. The tag tells
Raymond what to do next.

### `<goto>FILE</goto>` — Continue in Same Context

Transitions to the next state while **preserving** the current Claude Code
session. The agent keeps all context from previous steps.

```markdown
Implement the feature. When done, emit <goto>COMMIT.md</goto>
```

**Use when:** The next step needs to see what this step did (e.g., writing a
commit message after implementing code).

**Context:** Preserved. All prior conversation history is visible.

### `<reset>FILE</reset>` — Fresh Start

Discards the current session and starts the next state with **no context**.

```markdown
Create a plan in plan.md. When done, emit <reset>IMPLEMENT.md</reset>
```

**Use when:** Prior work is captured in files, not needed in context. Useful at
phase boundaries (planning → implementation) to keep context clean.

**Context:** Discarded. The return stack is also cleared.

**Working directory:** Supports an optional `cd` attribute to change the
agent's working directory:

```
<reset cd="/repo/worktree-feature">IMPLEMENT.md</reset>
```

### `<call return="NEXT">CHILD</call>` — Subroutine Call

Calls a child state that runs in a **branched context** (starts with the
caller's context, then accumulates its own). When the child emits `<result>`,
control returns to `NEXT` with the result payload.

```markdown
Delegate research:
<call return="SUMMARIZE.md">RESEARCH.md</call>
```

The child can iterate, make mistakes, accumulate noise — only the `<result>`
payload comes back. The caller's context stays clean.

**Use when:** A subtask may iterate or produce noise that would pollute the
parent's context.

**Context:** Child branches from caller. Caller's context is preserved and
resumed when the child returns.

### `<function return="NEXT">EVAL</function>` — Stateless Evaluation

Like `<call>`, but the child runs with **no context** (fresh session). Good
for evaluators and decision points.

```markdown
<function return="NEXT.md">EVALUATE.md</function>
```

**Use when:** You need a cheap, isolated evaluation. The task requires no
context beyond what's in its own prompt.

**Context:** Child starts fresh. Caller's context is preserved and resumed.

### `<fork next="NEXT" ...>WORKER</fork>` — Spawn Independent Agent

Spawns a new independent agent running `WORKER` while the current agent
continues at `NEXT`. The spawned agent has its own lifecycle and doesn't
return a result to the parent.

```markdown
<fork next="DISPATCH-ANOTHER.md" item="issue-123">WORKER.md</fork>
```

**Attributes:** Beyond the required `next`, any additional attributes become
template variables in the worker's prompt. The `cd` attribute sets the
worker's working directory and is not passed as a template variable.

```markdown
<fork next="CONTINUE.md" cd="/repo/worktree-a" task="refactor">WORKER.md</fork>
```

**Use when:** You want parallel execution. The parent doesn't wait for the
worker and doesn't receive its result.

### `<result>...</result>` — Return or Terminate

Returns a payload to the most recent caller (from `<call>` or `<function>`),
or terminates the agent if there's no caller.

```markdown
<result>Analysis complete: found 3 issues</result>
```

The payload text is passed as-is to the return state via `{{result}}`.

## Choosing the Right Pattern

| Question | If yes | If no |
|----------|--------|-------|
| Is this part of a larger workflow? | `call`, `goto`, `reset`, or `fork` | `function` |
| Should intermediate work be discarded? | `call` or `reset` | `goto` |
| Is this a decision/evaluation point? | `function` | Others |
| Will there be messy iterations? | `call` | `goto` or `reset` |
| Does the next step need this step's history? | `goto` | `function`, `call`, or `reset` |
| Is prior work saved to files? | `reset` | `goto` |
| Need parallel execution? | `fork` | Others |

### Combined Example

```
1. [function]  Evaluator: "Which issue to work on?" → "issue 195"
2. [goto]      Main session: "Work on issue 195" (receives result)
3. [call]      "Create and refine plan" (iterates 3x, returns plan)
4. [reset]     "Implement per plan-195.md" (fresh context, reads from file)
5. [call]      "Implement until tests pass" (iterates 5x, returns)
6. [goto]      "Review and commit" (preserves implementation context)
7. [function]  "Ready to commit?" → YES
8. [goto]      "Commit and close issue"
9. [result]    Workflow complete
```

Step 4 uses `<reset>` because the plan is in a file — no need to carry
planning iterations in context. Steps 6 and 8 use `<goto>` because the commit
message needs to see what was implemented.

## Template Variables

Prompts support `{{variable}}` placeholders that the orchestrator substitutes
before sending to Claude Code.

### `{{result}}` — Return Values

When a state is reached via a `<call>` or `<function>` return, the child's
`<result>` payload is available as `{{result}}`:

```markdown
The research findings:

{{result}}

Write a summary based on these findings.
```

The `--input` CLI flag sets `{{result}}` for the first state:

```bash
raymond workflow.md --input "hello, there"
```

### Fork Attributes

Extra attributes on `<fork>` tags become template variables in the worker:

```markdown
<!-- Parent emits: -->
<fork next="CONTINUE.md" item="issue-123" priority="high">WORKER.md</fork>
```

```markdown
<!-- WORKER.md receives: -->
Your assigned item is: {{item}}
Priority: {{priority}}
```

For shell scripts, fork attributes become environment variables instead:

```bash
echo "Processing item: $item"        # "issue-123"
echo "Priority level: $priority"     # "high"
```

**Note:** The `next` and `cd` attributes are consumed by the orchestrator and
are not available as template variables or environment variables.

## State Resolution

Transition targets can omit the file extension:

```markdown
<goto>POLL</goto>
```

The orchestrator resolves the name by checking (in order):

**On Unix:** `POLL.md` → `POLL.sh`

**On Windows:** `POLL.md` → `POLL.bat`

If both `.md` and a script exist for the same name on the same platform, that's
an ambiguity error. If you specify an explicit extension
(`<goto>POLL.sh</goto>`), no resolution occurs — that exact file must exist.

This means you can swap a state between markdown and script without updating
any transitions that reference it.

## Working Directory (`cd` Attribute)

By default, all agents execute in the directory where `raymond` was launched.
The `cd` attribute lets agents operate in different directories:

```markdown
<!-- Fork a worker into a different directory -->
<fork next="CONTINUE.md" cd="/repo/worktree-a">WORKER.md</fork>

<!-- Reset with a directory change -->
<reset cd="/repo/worktree-feature">IMPLEMENT.md</reset>
```

**Supported on:** `<fork>` (sets worker's directory) and `<reset>` (changes
current agent's directory).

**Not supported on:** `<goto>`, `<call>`, `<function>` — these continue or
branch existing sessions tied to the original directory.

Relative paths resolve against the agent's current working directory (or the
orchestrator's directory if none is set). Once set, the directory persists
across subsequent transitions until changed by another `<reset cd="...">`.

## Error Handling

### Markdown states with frontmatter

If the agent emits no tag, the wrong tag, or multiple tags, the orchestrator
generates a reminder listing all valid transitions and re-prompts (up to 3
retries). This is why frontmatter is recommended — it enables recovery.

### Markdown states without frontmatter

Missing or invalid tags are fatal. The orchestrator has no way to generate a
meaningful reminder without knowing the allowed transitions.

### Shell scripts

All errors are fatal. Scripts must emit exactly one valid tag on every code
path. There is no re-prompting for scripts.

## Configuration

### CLI Flags

```bash
raymond workflow.md                          # Defaults
raymond workflow.md --budget 5.0             # Cost limit ($5.00)
raymond workflow.md --model sonnet           # Default model for all states
raymond workflow.md --effort high            # Default effort level for all states
raymond workflow.md --dangerously-skip-permissions  # No permission prompts
raymond workflow.md --input "data"           # Initial {{result}} value
raymond workflow.md --no-debug               # Disable debug logging
raymond workflow.md --verbose                # Verbose output
```

### Config File

Create `.raymond/config.toml` to avoid repeating CLI flags:

```bash
raymond --init-config  # Generates config with all options commented out
```

```toml
[raymond]
budget = 50.0
model = "sonnet"
effort = "medium"
# dangerously_skip_permissions = false
```

CLI arguments override config file values. See
[configuration-file-design.md](configuration-file-design.md) for details.

## Complete Examples

### Pattern: Plan then Implement

```
workflows/coding/
  PLAN.md
  IMPLEMENT.md
```

**PLAN.md:**
```markdown
---
allowed_transitions:
  - { tag: reset, target: IMPLEMENT.md }
---
Read the requirements and create a detailed plan in plan.md.
When done, emit <reset>IMPLEMENT.md</reset>
```

**IMPLEMENT.md:**
```markdown
---
allowed_transitions:
  - { tag: goto, target: IMPLEMENT.md }
  - { tag: result }
---
Implement the feature per plan.md. Run the tests.
If tests fail, fix and retry: <goto>IMPLEMENT.md</goto>
When all tests pass: <result>Done</result>
```

### Pattern: Evaluator Decision Point

```markdown
---
model: haiku
allowed_transitions:
  - { tag: result }
---
Given the following test output, is the task complete?

{{result}}

Respond with <result>YES</result> or <result>NO</result>
```

### Pattern: Hybrid Script + LLM Workflow

```
workflows/monitor/
  POLL.sh       # Zero-cost polling loop
  PROCESS.md    # LLM reasoning when work is found
```

**POLL.sh:**
```bash
#!/bin/bash
response=$(curl -s "$API_URL/tasks?status=pending")
count=$(echo "$response" | jq 'length')

if [ "$count" -gt 0 ]; then
    echo "$response" > /tmp/pending_tasks.json
    echo "<goto>PROCESS.md</goto>"
else
    sleep 60
    echo "<reset>POLL.sh</reset>"
fi
```

**PROCESS.md:**
```markdown
---
allowed_transitions:
  - { tag: reset, target: POLL.sh }
  - { tag: result }
---
Read /tmp/pending_tasks.json and process the tasks.
When done, go back to polling: <reset>POLL.sh</reset>
If there's nothing more to do: <result>All tasks processed</result>
```

### Pattern: Fork Workers

```markdown
<!-- DISPATCH.md -->
Read items.txt. For each item, spawn a worker:
<fork next="DISPATCH.md" item="item1">WORKER.md</fork>

<!-- WORKER.md -->
---
allowed_transitions:
  - { tag: result }
---
Process item: {{item}}
When done: <result>Processed {{item}}</result>
```

For more complete examples, see [sample-workflows.md](sample-workflows.md).
